package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

var (
	lastApplyStatuses = []v1.StackStatus{v1.StackApplying, v1.StackApplied, v1.StackFailed, v1.StackNoop, v1.StackUnknown, v1.StackUnconfirmed}
	lastPlanStatuses  = []v1.StackStatus{v1.StackPlanning, v1.StackPlanned, v1.StackFailed, v1.StackUnknown, v1.StackUnconfirmed}
)

type family struct {
	key      string
	versions []store.ModuleVersion
}

type detailBuilder struct {
	s          *server
	repo       store.Repo
	dependsOn  map[string][]string
	dependents map[string][]string
	locks      map[uuid.UUID]store.Lock
	drift      map[uuid.UUID]store.Drift
	families   map[uuid.UUID]*family
}

func (s *server) currentGraph(ctx context.Context, repoID int64) (*v1.Graph, uuid.UUID, error) {
	g, id, err := s.db.GetDefaultGraph(ctx, repoID)
	if errors.Is(err, store.ErrNotFound) {
		g, id, err = s.db.LatestGraph(ctx, repoID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, uuid.Nil, nil
	}
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("load current graph: %w", err)
	}
	return g, id, nil
}

func (s *server) newDetailBuilder(ctx context.Context, repo store.Repo, bulk bool) (*detailBuilder, error) {
	b := &detailBuilder{
		s:          s,
		repo:       repo,
		dependsOn:  map[string][]string{},
		dependents: map[string][]string{},
		families:   map[uuid.UUID]*family{},
	}
	g, _, err := s.currentGraph(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	if g != nil {
		for _, e := range g.Edges {
			if (e.Type != v1.EdgeDependsOn && e.Type != v1.EdgeReadsState) || e.From.Kind != v1.NodeStack || e.To.Kind != v1.NodeStack {
				continue
			}
			b.dependsOn[e.From.Key] = append(b.dependsOn[e.From.Key], e.To.Key)
			b.dependents[e.To.Key] = append(b.dependents[e.To.Key], e.From.Key)
		}
	}
	if !bulk {
		return b, nil
	}
	locks, err := s.db.ListLocks(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("list locks: %w", err)
	}
	b.locks = make(map[uuid.UUID]store.Lock, len(locks))
	for _, l := range locks {
		b.locks[l.StackID] = l
	}
	drift, err := s.db.LatestDriftForRepo(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("list drift: %w", err)
	}
	b.drift = make(map[uuid.UUID]store.Drift, len(drift))
	for _, d := range drift {
		b.drift[d.StackID] = d
	}
	return b, nil
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (b *detailBuilder) detail(ctx context.Context, st store.Stack) (v1.StackDetail, error) {
	d := st.Detail()
	var err error
	if d.LastApply, err = b.lastRun(ctx, st.ID, v1.ModeApply, lastApplyStatuses); err != nil {
		return d, err
	}
	if d.LastPlan, err = b.lastRun(ctx, st.ID, v1.ModePlan, lastPlanStatuses); err != nil {
		return d, err
	}
	if d.Drift, err = b.driftOf(ctx, st.ID); err != nil {
		return d, err
	}
	if d.Lock, err = b.lockOf(ctx, st.ID); err != nil {
		return d, err
	}
	d.DependsOn = sortedUnique(b.dependsOn[st.Key])
	d.Dependents = sortedUnique(b.dependents[st.Key])
	if d.Modules, err = b.modules(ctx, st.ID); err != nil {
		return d, err
	}
	return d, nil
}

func (b *detailBuilder) lastRun(ctx context.Context, stackID uuid.UUID, mode v1.RunMode, statuses []v1.StackStatus) (*v1.RunStackRef, error) {
	row, err := b.s.db.LatestRunStackForStack(ctx, stackID, mode, statuses...)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last %s of stack: %w", mode, err)
	}
	ref := row.ToV1()
	return &ref, nil
}

func (b *detailBuilder) driftOf(ctx context.Context, stackID uuid.UUID) (*v1.DriftStatus, error) {
	var (
		d   store.Drift
		err error
	)
	if b.drift != nil {
		var ok bool
		if d, ok = b.drift[stackID]; !ok {
			return nil, nil
		}
	} else if d, err = b.s.db.LatestDrift(ctx, stackID); errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("latest drift of stack: %w", err)
	}
	out := d.ToV1()
	if out.IssueNumber > 0 {
		out.IssueURL = b.s.webURL + "/" + b.repo.FullName + "/issues/" + strconv.Itoa(out.IssueNumber)
	}
	return &out, nil
}

func (b *detailBuilder) lockOf(ctx context.Context, stackID uuid.UUID) (*v1.LockInfo, error) {
	var (
		l   store.Lock
		err error
	)
	if b.locks != nil {
		var ok bool
		if l, ok = b.locks[stackID]; !ok {
			return nil, nil
		}
	} else if l, err = b.s.db.GetLock(ctx, stackID); errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("lock of stack: %w", err)
	}
	out := l.ToV1()
	return &out, nil
}

func (b *detailBuilder) modules(ctx context.Context, stackID uuid.UUID) ([]v1.ModuleConsume, error) {
	rows, err := b.s.db.StackModules(ctx, stackID)
	if err != nil {
		return nil, fmt.Errorf("modules of stack: %w", err)
	}
	out := make([]v1.ModuleConsume, 0, len(rows))
	for _, row := range rows {
		fam, err := b.family(ctx, row.ModuleID)
		if err != nil {
			return nil, err
		}
		latest, behind := versionLag(fam.versions, row.Ref)
		out = append(out, v1.ModuleConsume{ModuleKey: fam.key, Ref: row.Ref, Latest: latest, Behind: behind})
	}
	slices.SortFunc(out, func(a, b v1.ModuleConsume) int {
		return cmp.Or(cmp.Compare(a.ModuleKey, b.ModuleKey), cmp.Compare(a.Ref, b.Ref))
	})
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (b *detailBuilder) family(ctx context.Context, moduleID uuid.UUID) (*family, error) {
	if f, ok := b.families[moduleID]; ok {
		return f, nil
	}
	m, err := b.s.db.GetModule(ctx, moduleID)
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", moduleID, err)
	}
	versions, err := b.s.db.ListModuleVersions(ctx, moduleID)
	if err != nil {
		return nil, fmt.Errorf("versions of module %s: %w", m.BaseKey, err)
	}
	f := &family{key: m.BaseKey, versions: versions}
	b.families[moduleID] = f
	return f, nil
}

func (s *server) visibleStack(r *http.Request, id identity) (store.Stack, store.Repo, error) {
	stackID, err := parseID(r.PathValue("id"), "stack")
	if err != nil {
		return store.Stack{}, store.Repo{}, err
	}
	missing := notFound("stack " + r.PathValue("id") + " not found")
	st, err := s.db.GetStack(r.Context(), stackID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Stack{}, store.Repo{}, missing
	}
	if err != nil {
		return store.Stack{}, store.Repo{}, fmt.Errorf("get stack: %w", err)
	}
	repo, err := s.db.GetRepo(r.Context(), st.RepoID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Stack{}, store.Repo{}, missing
	}
	if err != nil {
		return store.Stack{}, store.Repo{}, fmt.Errorf("get repository: %w", err)
	}
	if !id.sees(repo.Account) {
		return store.Stack{}, store.Repo{}, missing
	}
	return st, repo, nil
}

func (s *server) stack(w http.ResponseWriter, r *http.Request, id identity) error {
	st, repo, err := s.visibleStack(r, id)
	if err != nil {
		return err
	}
	b, err := s.newDetailBuilder(r.Context(), repo, false)
	if err != nil {
		return err
	}
	d, err := b.detail(r.Context(), st)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, d)
	return nil
}

func (s *server) stackRuns(w http.ResponseWriter, r *http.Request, id identity) error {
	st, _, err := s.visibleStack(r, id)
	if err != nil {
		return err
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	rows, next, err := s.db.StackHistory(r.Context(), st.ID, limit, cursor)
	if err != nil {
		return storeCursorError(err)
	}
	page := v1.Page[v1.RunStackRef]{Items: make([]v1.RunStackRef, len(rows)), NextCursor: next}
	for i, row := range rows {
		page.Items[i] = row.ToV1()
	}
	s.writeJSON(w, r, http.StatusOK, page)
	return nil
}

func (s *server) repoStacks(w http.ResponseWriter, r *http.Request, id identity) error {
	repo, err := s.visibleRepo(r, id)
	if err != nil {
		return err
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	stacks, err := s.db.ListStacks(r.Context(), repo.ID, false)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}
	items, next, err := pageByKey(stacks, func(st store.Stack) string { return st.Key }, limit, cursor)
	if err != nil {
		return err
	}
	b, err := s.newDetailBuilder(r.Context(), repo, true)
	if err != nil {
		return err
	}
	page := v1.Page[v1.StackDetail]{Items: make([]v1.StackDetail, len(items)), NextCursor: next, Total: len(stacks)}
	for i, st := range items {
		if page.Items[i], err = b.detail(r.Context(), st); err != nil {
			return err
		}
	}
	s.writeJSON(w, r, http.StatusOK, page)
	return nil
}
