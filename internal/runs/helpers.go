package runs

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/gh/codeowners"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/store"
)

var displayTitle = regexp.MustCompile(`^stackorder (plan|apply|drift) ([0-9a-fA-F-]{36}) wave (\d+)$`)

type dispatchTitle struct {
	Mode  v1.RunMode
	RunID uuid.UUID
	Wave  int
}

func parseDisplayTitle(title string) (dispatchTitle, bool) {
	m := displayTitle.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return dispatchTitle{}, false
	}
	wave, werr := strconv.Atoi(m[3])
	id, ierr := uuid.Parse(m[2])
	if werr != nil || ierr != nil {
		return dispatchTitle{}, false
	}
	return dispatchTitle{Mode: v1.RunMode(m[1]), RunID: id, Wave: wave}, true
}

func formatDisplayTitle(mode v1.RunMode, runID string, wave int) string {
	return "stackorder " + string(mode) + " " + runID + " wave " + strconv.Itoa(wave)
}

func jobStackValue(name string) (string, bool) {
	open := strings.IndexByte(name, '(')
	if open < 0 {
		fields := strings.Fields(name)
		if len(fields) < 2 {
			return "", false
		}
		return fields[len(fields)-1], true
	}
	rest := name[open+1:]
	end := strings.IndexAny(rest, ",)")
	if end < 0 {
		return "", false
	}
	v := strings.TrimSpace(rest[:end])
	return v, v != ""
}

func matchJobStack(name string, rows []store.RunStack) (store.RunStack, bool) {
	v, ok := jobStackValue(name)
	if !ok {
		return store.RunStack{}, false
	}
	for _, rs := range rows {
		if rs.Key == v {
			return rs, true
		}
	}
	var byPath []store.RunStack
	for _, rs := range rows {
		if rs.Path == v {
			byPath = append(byPath, rs)
		}
	}
	if len(byPath) == 1 {
		return byPath[0], true
	}
	return store.RunStack{}, false
}

func approvers(reviews []gh.Review, sha, author string) []string {
	state := map[string]bool{}
	var order []string
	for _, r := range reviews {
		login := strings.ToLower(r.User.Login)
		if login == "" || r.CommitID != sha || strings.EqualFold(login, author) {
			continue
		}
		switch strings.ToUpper(r.State) {
		case gh.ReviewApproved:
			if _, seen := state[login]; !seen {
				order = append(order, login)
			}
			state[login] = true
		case gh.ReviewChangesRequested, gh.ReviewDismissed:
			if _, seen := state[login]; !seen {
				order = append(order, login)
			}
			state[login] = false
		}
	}
	out := make([]string, 0, len(order))
	for _, login := range order {
		if state[login] {
			out = append(out, login)
		}
	}
	slices.Sort(out)
	return out
}

type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var out semver
	core := v
	if i := strings.IndexByte(v, '-'); i >= 0 {
		core, out.pre = v[:i], v[i+1:]
		if out.pre == "" {
			return semver{}, false
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		nums[i] = n
	}
	out.major, out.minor, out.patch = nums[0], nums[1], nums[2]
	return out, true
}

func (a semver) compare(b semver) int {
	if c := cmp.Or(cmp.Compare(a.major, b.major), cmp.Compare(a.minor, b.minor), cmp.Compare(a.patch, b.patch)); c != 0 {
		return c
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return comparePrerelease(a.pre, b.pre)
}

func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aerr == nil && berr == nil:
			c = cmp.Compare(an, bn)
		case aerr == nil:
			c = -1
		case berr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

func versionsBehind(released []string, ref string) (int, bool) {
	pinned, ok := parseSemver(ref)
	if !ok {
		return 0, false
	}
	n := 0
	for _, v := range released {
		if sv, ok := parseSemver(v); ok && sv.pre == "" && sv.compare(pinned) > 0 {
			n++
		}
	}
	return n, true
}

func stackCodeowners(f *codeowners.File, dir string) []string {
	if f == nil {
		return nil
	}
	return f.OwnersFor(strings.Trim(dir, "/") + "/")
}

func noopCandidate(reasons []v1.Reason, summary *v1.PlanSummary, hasChanges bool) bool {
	if len(reasons) == 0 || hasChanges || (summary != nil && !summary.Empty()) {
		return false
	}
	for _, r := range reasons {
		if r != v1.ReasonDependent && r != v1.ReasonReadsState {
			return false
		}
	}
	return true
}

func blockedByFailure(g *v1.Graph, rows []store.RunStack, failed store.RunStack) []store.RunStack {
	if g == nil {
		return nil
	}
	dependents := graph.Dependents(g, []string{failed.Key}, v1.EdgeDependsOn, v1.EdgeReadsState)
	var out []store.RunStack
	for _, rs := range rows {
		if rs.Wave <= failed.Wave || !slices.Contains(dependents, rs.Key) {
			continue
		}
		switch {
		case rs.Status == v1.StackBlocked:
			if slices.Contains(rs.BlockedBy, failed.Key) {
				continue
			}
		case rs.Status.Terminal():
			continue
		}
		rs.BlockedBy = slices.Sorted(slices.Values(append(slices.Clone(rs.BlockedBy), failed.Key)))
		rs.BlockedBy = slices.Compact(rs.BlockedBy)
		out = append(out, rs)
	}
	return out
}

func planDone(s v1.StackStatus) bool { return s == v1.StackPlanned || s.Terminal() }

func brokenStatus(s v1.StackStatus) bool {
	return s == v1.StackFailed || s == v1.StackBlocked || s == v1.StackUnknown
}

func planTarget(rows []store.RunStack) v1.RunStatus {
	var broken, unconfirmed bool
	for _, rs := range rows {
		switch {
		case !planDone(rs.Status):
			return v1.RunPlanning
		case brokenStatus(rs.Status):
			broken = true
		case rs.Status == v1.StackUnconfirmed:
			unconfirmed = true
		}
	}
	switch {
	case broken:
		return v1.RunFailed
	case unconfirmed:
		return v1.RunUnconfirmed
	}
	return v1.RunPlanned
}

func applyBroken(rows []store.RunStack) bool {
	for _, rs := range rows {
		if brokenStatus(rs.Status) || rs.Status == v1.StackUnconfirmed {
			return true
		}
	}
	return false
}

func waveDone(rows []store.RunStack, wave int) bool {
	for _, rs := range rows {
		if rs.Wave == wave && !rs.Status.Terminal() {
			return false
		}
	}
	return true
}

func lastWave(rows []store.RunStack) int {
	last := 0
	for _, rs := range rows {
		last = max(last, rs.Wave)
	}
	return last
}

func chunk[T any](items []T, size int) [][]T {
	if size <= 0 {
		size = len(items)
	}
	var out [][]T
	for len(items) > 0 {
		n := min(size, len(items))
		out = append(out, items[:n])
		items = items[n:]
	}
	return out
}

func cutUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func subsetWaves(g *v1.Graph, all, selected []string) map[string]int {
	member := map[string]bool{}
	for _, k := range selected {
		member[k] = true
	}
	domain := map[string]bool{}
	for _, k := range all {
		domain[k] = true
	}
	out := map[v1.NodeRef][]string{}
	if g != nil {
		for _, e := range g.Edges {
			if (e.Type == v1.EdgeDependsOn || e.Type == v1.EdgeReadsState) && e.From.Kind == v1.NodeStack && e.To.Kind == v1.NodeStack {
				out[e.From] = append(out[e.From], e.To.Key)
			}
		}
	}
	synthetic := &v1.Graph{}
	for _, x := range slices.Sorted(slices.Values(selected)) {
		seen := map[string]bool{x: true}
		stack := slices.Clone(out[v1.StackRef(x)])
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[n] {
				continue
			}
			seen[n] = true
			switch {
			case member[n]:
				synthetic.Edges = append(synthetic.Edges, v1.Edge{From: v1.StackRef(x), To: v1.StackRef(n), Type: v1.EdgeDependsOn})
			case domain[n]:
				stack = append(stack, out[v1.StackRef(n)]...)
			}
		}
	}
	waves, cycles := graph.Waves(synthetic, selected)
	result := map[string]int{}
	if len(cycles) > 0 {
		return result
	}
	for i, keys := range waves {
		for _, k := range keys {
			result[k] = i
		}
	}
	return result
}
