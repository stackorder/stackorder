package runs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/stateread"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	codeNotFound  = "not_found"
	maxAuditField = 256
)

type outputsRead struct {
	p      principal.Principal
	repo   string
	key    string
	runID  string
	reader string

	readerRepo store.Repo
	producer   *store.Repo
}

// StackOutputs serves the root module outputs of stack key of repo to a
// reader its default-branch configuration lists, reading the stack's state
// object with the server's AWS credentials. Nothing read is stored or
// logged.
//
// A runner job names the run it belongs to and the stack it runs as reader;
// that stack must be one whose job in the run has not reported yet, in the
// workflow run bound to its dispatch for a dispatched job, and the run must
// be neither superseded nor finished. A read binds a workflow run no
// dispatch holds yet only when its jobs name a stack of the reader's
// dispatch. The producer must belong to the reader's installation and be a
// stack its default branch still has at the commit the stored
// default-branch stackorder.yaml was read at: that configuration discovers
// the key's directory, the directory exists, and the key's instance is one
// of config.InstanceNames for the directory's .stackorder.yaml and, under
// stacks.instances.from_var_files, the var files of that commit. A merged
// pull request's graph plays no part. Its outputs.readers,
// resolved from that stackorder.yaml and .stackorder.yaml, must list the
// reader; refusals across repositories answer as if the stack did not
// exist. An API key is an admin: it may name a run and a reader, which are
// only checked for form, and reads whatever readers say. The state object
// is the one an apply or drift job last reported for the stack, recorded
// and read only when a grant of the operator covers it for the producer's
// repository, and sensitive outputs are withheld, by name, unless
// outputs.sensitive is share. Refusals are *principal.CodedError values or
// wrap the principal sentinels; reads, refusals before the state is read
// and locations no grant covers are audited with counts and names cut to a
// bounded length, never values.
func (s *Service) StackOutputs(ctx context.Context, p principal.Principal, repo, key, runID, reader string) (*v1.StackOutputs, error) {
	if s.state == nil {
		return nil, &principal.CodedError{Kind: principal.ErrNotFound, Code: v1.ErrCodeOutputsDisabled,
			Message: "this server does not serve stack outputs; its operator has not listed any state buckets"}
	}
	r := &outputsRead{p: p, repo: repo, key: key, runID: runID, reader: reader}
	eff, loc, err := s.authorizeOutputs(ctx, r)
	if err != nil {
		s.auditOutputsRefused(ctx, r, err)
		return nil, err
	}
	st, err := s.state.Read(ctx, r.producer.FullName, loc)
	if errors.Is(err, stateread.ErrNotAllowed) {
		err = s.locationNotGranted(ctx, r, loc)
		s.auditOutputsRefused(ctx, r, err)
		return nil, err
	}
	if err != nil {
		return nil, s.stateError(ctx, r, err)
	}
	if st == nil {
		return nil, s.stateError(ctx, r, stateread.ErrUnreadable)
	}
	out, sensitive := servedOutputs(r.producer.FullName, key, st, eff.OutputSensitive == v1.OutputsShare)
	details := r.details()
	details["outputs"], details["sensitive"], details["withheld"] = len(out.Outputs), sensitive, len(out.Withheld)
	s.audit(ctx, p.Actor(), "outputs_read", r.target(), details)
	return out, nil
}

func (s *Service) authorizeOutputs(ctx context.Context, r *outputsRead) (config.Effective, stateread.Location, error) {
	if err := s.bindReader(ctx, r); err != nil {
		return config.Effective{}, stateread.Location{}, err
	}
	producer, err := s.outputsProducer(ctx, r)
	if err != nil {
		return config.Effective{}, stateread.Location{}, err
	}
	stack, err := s.producerStack(ctx, r, producer)
	if err != nil {
		return config.Effective{}, stateread.Location{}, err
	}
	eff, err := s.outputsPolicy(ctx, r, producer)
	if err != nil {
		return config.Effective{}, stateread.Location{}, err
	}
	if stack.StateObject == nil {
		why := "the stack has had no apply or drift job on a CLI that reports its state object"
		if stack.StateObjectAt != nil {
			why = "its last apply or drift job reported a state object outside the buckets and prefixes this server may read for " + producer.FullName
		}
		return config.Effective{}, stateread.Location{}, &principal.CodedError{Kind: principal.ErrConflict, Code: v1.ErrCodeStateLocationUnknown,
			Message: fmt.Sprintf("the server does not know where the state of %s is: %s", r.key, why)}
	}
	obj := stack.StateObject
	return eff, stateread.Location{Bucket: obj.Bucket, Key: obj.Key, Region: obj.Region}, nil
}

func (s *Service) bindReader(ctx context.Context, r *outputsRead) error {
	switch r.p.Kind {
	case principal.APIKey:
		if r.runID != "" {
			if _, err := parseRunID(r.runID); err != nil {
				return err
			}
		}
		if r.reader != "" && !validStackKey(r.reader) {
			return &principal.InvalidError{Field: "reader", Reason: fmt.Sprintf("%q is not a stack key", r.reader)}
		}
		return nil
	case principal.OIDC:
	default:
		return principal.Wrap(principal.ErrForbidden, "%s principals cannot read stack outputs", r.p.Kind)
	}
	c, err := claimsOf(r.p)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(r.runID) == "":
		return principal.Wrap(principal.ErrForbidden, "a runner job reads stack outputs for the run it belongs to; name the run id")
	case r.reader == "":
		return principal.Wrap(principal.ErrForbidden, "a runner job reads stack outputs as a stack of its run; name the reading stack")
	case !validStackKey(r.reader):
		return principal.Wrap(principal.ErrForbidden, "a runner job reads stack outputs as a stack of its run; the reader is not a stack key")
	}
	run, repo, err := s.loadRun(ctx, r.runID)
	if errors.Is(err, principal.ErrInvalid) || errors.Is(err, principal.ErrNotFound) {
		return principal.Wrap(principal.ErrForbidden, "%q is not a run; reading stack outputs needs the id of the run the job belongs to", r.runID)
	}
	if err != nil {
		return err
	}
	if err := checkRepoClaims(c, repo); err != nil {
		return err
	}
	r.readerRepo = repo
	switch {
	case run.Status == v1.RunSuperseded:
		return principal.Wrap(principal.ErrSuperseded, "run %s was superseded by a newer commit", run.ID)
	case c.EventName == eventPullRequest && planFinished(run.Status):
		return finishedPlanError(run)
	case run.Status.Terminal():
		return principal.Wrap(principal.ErrConflict, "run %s is %s; a finished run reads no stack outputs", run.ID, run.Status)
	}
	row, err := s.readerRow(ctx, run, r.reader)
	if err != nil {
		return err
	}
	if err := readerRunning(run, row, c); err != nil {
		return err
	}
	_, err = s.authorizeJob(ctx, c, run, repo, &row, true)
	return err
}

func (s *Service) readerRow(ctx context.Context, run store.Run, reader string) (store.RunStack, error) {
	notInRun := principal.Wrap(principal.ErrForbidden, "%s is not a stack of run %s", reader, run.ID)
	stack, err := s.st.GetStackByKey(ctx, run.RepoID, reader)
	if errors.Is(err, store.ErrNotFound) {
		return store.RunStack{}, notInRun
	}
	if err != nil {
		return store.RunStack{}, storeErr(err, "stack %s", reader)
	}
	row, err := s.st.GetRunStack(ctx, run.ID, stack.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.RunStack{}, notInRun
	}
	if err != nil {
		return store.RunStack{}, storeErr(err, "stack %s in run %s", reader, run.ID)
	}
	return row, nil
}

func readerRunning(run store.Run, row store.RunStack, c *oidc.Claims) error {
	if c.EventName == eventWorkflowDispatch && row.DispatchID == nil {
		return principal.Wrap(principal.ErrForbidden, "%s has not been dispatched in run %s, so no job runs as it", row.Key, run.ID)
	}
	running := []v1.StackStatus{v1.StackPending, v1.StackPlanning, v1.StackUnknown}
	if run.Mode == v1.ModeApply {
		running = updatableStatuses(v1.ModeApply)
	}
	if !slices.Contains(running, row.Status) {
		return principal.Wrap(principal.ErrForbidden, "%s is %s in run %s; only a stack whose job has not reported reads stack outputs", row.Key, row.Status, run.ID)
	}
	return nil
}

func (s *Service) outputsProducer(ctx context.Context, r *outputsRead) (store.Repo, error) {
	repo, err := s.st.GetRepoByName(ctx, r.repo)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Repo{}, r.notFound()
	case err != nil:
		return store.Repo{}, storeErr(err, "repository %s", r.repo)
	case repo.Suspended:
		return store.Repo{}, r.notFound()
	case r.p.Kind == principal.OIDC && repo.InstallationID != r.readerRepo.InstallationID:
		return store.Repo{}, r.notFound()
	}
	if r.p.Kind == principal.APIKey {
		r.readerRepo = repo
	}
	r.producer = &repo
	return repo, nil
}

func (s *Service) producerStack(ctx context.Context, r *outputsRead, producer store.Repo) (store.Stack, error) {
	if !validStackKey(r.key) {
		return store.Stack{}, r.notFound()
	}
	stack, err := s.st.GetStackByKey(ctx, producer.ID, r.key)
	if errors.Is(err, store.ErrNotFound) {
		return store.Stack{}, r.notFound()
	}
	if err != nil {
		return store.Stack{}, storeErr(err, "stack %s of %s", r.key, producer.FullName)
	}
	return stack, nil
}

func (s *Service) outputsPolicy(ctx context.Context, r *outputsRead, producer store.Repo) (config.Effective, error) {
	sameRepo := r.readerRepo.ID == producer.ID
	root := repoConfig(producer)
	dir, _ := keyDir(r.key, "")
	sc, err := s.producerOnDefaultBranch(ctx, r, producer, root)
	if err != nil {
		return config.Effective{}, err
	}
	eff, err := defaultEffective(root, map[string]*v1.StackConfig{dir: sc}, r.key, "")
	if err != nil {
		return config.Effective{}, r.unresolved(sameRepo, err)
	}
	if r.p.Kind == principal.APIKey || config.ReaderAllowed(eff.OutputReaders, producer.FullName, r.readerRepo.FullName, r.reader) {
		return eff, nil
	}
	if !sameRepo {
		return config.Effective{}, r.notFound()
	}
	return config.Effective{}, &principal.CodedError{Kind: principal.ErrForbidden, Code: v1.ErrCodeOutputsNotShared,
		Message: fmt.Sprintf("%s does not share its outputs with %s; list it under outputs.readers in %s or %s on the default branch",
			r.key, r.reader, path.Join(dir, config.StackFile), config.RootFile)}
}

func (s *Service) producerOnDefaultBranch(ctx context.Context, r *outputsRead, producer store.Repo, root *v1.RepoConfig) (*v1.StackConfig, error) {
	dir, instance := keyDir(r.key, "")
	if !discovers(root, dir) {
		return nil, r.notFound()
	}
	c, err := s.client(ctx, producer)
	if err != nil {
		return nil, err
	}
	data, found, err := s.defaultStackFile(ctx, c, producer, dir)
	if err != nil {
		return nil, err
	}
	if !found {
		_, exists, err := s.defaultDir(ctx, c, producer, dir)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, r.notFound()
		}
	}
	sameRepo := r.readerRepo.ID == producer.ID
	var sc *v1.StackConfig
	if found {
		if sc, err = config.ParseStack(data); err != nil {
			return nil, r.unresolved(sameRepo, err)
		}
	}
	var matched []string
	if sc == nil || len(sc.Instances) == 0 {
		tree := &defaultTree{ctx: ctx, s: s, c: c, repo: producer, dir: dir}
		matched, err = config.MatchVarFilesFS(root, tree)
		if tree.err != nil {
			return nil, tree.err
		}
		if err != nil {
			return nil, r.unresolved(sameRepo, err)
		}
	}
	names, err := config.InstanceNames(root, dir, sc, matched)
	if err != nil {
		return nil, r.unresolved(sameRepo, err)
	}
	if !slices.Contains(names, instance) {
		return nil, r.notFound()
	}
	return sc, nil
}

func (r *outputsRead) unresolved(sameRepo bool, err error) error {
	if !sameRepo {
		return r.notFound()
	}
	return &principal.CodedError{Kind: principal.ErrForbidden, Code: v1.ErrCodeOutputsNotShared,
		Message: fmt.Sprintf("the outputs configuration of %s on the default branch does not resolve, so its outputs are not shared: %v", r.key, err)}
}

func (r *outputsRead) notFound() error {
	return &principal.CodedError{Kind: principal.ErrNotFound, Code: codeNotFound,
		Message: fmt.Sprintf("%s has no stack %s whose outputs this caller may read", r.repo, r.key)}
}

func validStackKey(key string) bool {
	return key != "" && !strings.Contains(key, "//") && config.NormalizePath(key) == key &&
		key != ".." && !strings.HasPrefix(key, "../")
}

var stateErrors = []struct {
	err  error
	kind error
	code string
	text string
}{
	{stateread.ErrNotFound, principal.ErrNotFound, v1.ErrCodeStateNotFound, "the state object of %s does not exist"},
	{stateread.ErrAccessDenied, principal.ErrForbidden, v1.ErrCodeStateAccessDenied, "the server is denied access to the state object of %s"},
	{stateread.ErrEncrypted, principal.ErrUnprocessable, v1.ErrCodeStateEncrypted, "the state of %s is encrypted, and the server does not decrypt state"},
	{stateread.ErrUnsupported, principal.ErrUnprocessable, v1.ErrCodeStateUnsupported, "the state of %s has a format version the server does not read"},
	{stateread.ErrTooLarge, principal.ErrUnprocessable, v1.ErrCodeStateTooLarge, "the state of %s or its outputs exceed the server's limits"},
	{stateread.ErrMalformed, principal.ErrUnprocessable, v1.ErrCodeStateUnreadable, "the server could not decode the state of %s"},
	{stateread.ErrUnreadable, principal.ErrUnprocessable, v1.ErrCodeStateUnreadable, "the server could not read the state object of %s"},
}

func (s *Service) locationNotGranted(ctx context.Context, r *outputsRead, loc stateread.Location) error {
	s.log.WarnContext(ctx, "refuse a state object no grant of its repository covers",
		"repo", r.producer.FullName, "stack", auditField(r.key), "bucket", auditField(loc.Bucket), "key", auditField(loc.Key))
	return &principal.CodedError{Kind: principal.ErrForbidden, Code: v1.ErrCodeStateBucketNotAllowed,
		Message: fmt.Sprintf("the state object of %s lies outside the buckets and prefixes this server may read for %s", r.key, r.producer.FullName)}
}

func (s *Service) stateError(ctx context.Context, r *outputsRead, err error) error {
	for _, e := range stateErrors {
		if errors.Is(err, e.err) {
			return &principal.CodedError{Kind: e.kind, Code: e.code, Message: fmt.Sprintf(e.text, r.key)}
		}
	}
	s.log.WarnContext(ctx, "read stack outputs", "repo", r.producer.FullName, "stack", r.key, "error", err)
	return principal.Wrap(principal.ErrUnavailable, "the state object of %s could not be read; try again", r.key)
}

func servedOutputs(repo, key string, st *stateread.State, shareSensitive bool) (*v1.StackOutputs, int) {
	out := &v1.StackOutputs{
		Repo: repo, Stack: key, Serial: st.Serial, Lineage: st.Lineage, TerraformVersion: st.TerraformVersion,
		Outputs: make(map[string]v1.StackOutput, len(st.Outputs)),
	}
	sensitive := 0
	for _, name := range slices.Sorted(maps.Keys(st.Outputs)) {
		o := st.Outputs[name]
		if o.Sensitive && !shareSensitive {
			out.Withheld = append(out.Withheld, name)
			continue
		}
		if o.Sensitive {
			sensitive++
		}
		out.Outputs[name] = v1.StackOutput{Value: o.Value, Type: o.Type, Sensitive: o.Sensitive}
	}
	return out, sensitive
}

func (s *Service) auditOutputsRefused(ctx context.Context, r *outputsRead, err error) {
	reason := refusalReason(err)
	if reason == "" {
		return
	}
	details := r.details()
	details["reason"] = reason
	s.audit(ctx, r.p.Actor(), "outputs_refused", r.target(), details)
}

func refusalReason(err error) string {
	var coded *principal.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	for _, k := range []struct {
		err  error
		code string
	}{
		{principal.ErrSuperseded, "superseded"},
		{principal.ErrForbidden, "forbidden"},
		{principal.ErrNotFound, codeNotFound},
		{principal.ErrConflict, "conflict"},
		{principal.ErrInvalid, "invalid"},
	} {
		if errors.Is(err, k.err) {
			return k.code
		}
	}
	return ""
}

func (r *outputsRead) producerName() string {
	if r.producer != nil {
		return r.producer.FullName
	}
	return r.repo
}

func (r *outputsRead) target() string {
	return v1.QualifiedStackKey(auditField(r.producerName()), auditField(r.key))
}

func (r *outputsRead) details() map[string]any {
	scope, readerRepo := r.repo, ""
	switch {
	case r.p.Kind == principal.OIDC && r.p.Claims != nil:
		scope, readerRepo = r.p.Claims.Repository, r.p.Claims.Repository
	case r.reader != "":
		readerRepo = r.producerName()
	}
	if r.producer != nil {
		scope = r.producer.FullName
	}
	return map[string]any{
		"repo": auditField(scope), "stack": auditField(r.key), "reader_repo": auditField(readerRepo),
		"reader": auditField(r.reader), "run_id": auditField(r.runID),
	}
}

func auditField(v string) string {
	if len(v) <= maxAuditField {
		return v
	}
	cut := maxAuditField
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}
