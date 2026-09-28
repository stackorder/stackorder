package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var (
	testID    = uuid.MustParse("7d9f6a3e-4b1c-4f2e-9a8b-1c2d3e4f5a6b")
	testOther = uuid.MustParse("0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e")
	testTime  = time.Date(2026, 9, 28, 12, 30, 0, 123456000, time.FixedZone("CEST", 2*60*60))
)

func TestRunToV1(t *testing.T) {
	started := testTime.Add(time.Minute)
	cases := []struct {
		name string
		in   Run
		want v1.Run
	}{
		{
			name: "full",
			in: Run{
				ID: testID, Repo: "acme/infra", SHA: "s", BaseSHA: "b", PRNumber: 4,
				Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunApplying, RequestedBy: "octocat",
				Waves: 3, CurrentWave: 1, Warnings: []string{"w"}, CreatedAt: testTime, StartedAt: &started,
			},
			want: v1.Run{
				ID: testID.String(), Repo: "acme/infra", SHA: "s", BaseSHA: "b", PRNumber: 4,
				Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunApplying, RequestedBy: "octocat",
				Waves: 3, CurrentWave: 1, Warnings: []string{"w"}, CreatedAt: testTime.UTC(), StartedAt: ptrTo(started.UTC()),
			},
		},
		{
			name: "empty warnings are omitted",
			in:   Run{ID: testID, Warnings: []string{}, CreatedAt: testTime},
			want: v1.Run{ID: testID.String(), CreatedAt: testTime.UTC()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.ToV1()
			assert.Equal(t, tc.want, got)
			assert.Equal(t, time.UTC, got.CreatedAt.Location())
		})
	}
}

func TestRowConversions(t *testing.T) {
	exit := 2
	summary := &v1.PlanSummary{Adds: 1}
	backend := &v1.Backend{Type: "s3", Bucket: "b", Key: "k"}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{
			name: "run stack",
			got: RunStack{
				RunID: testOther, StackID: testID, Key: "stacks/a:blue", Path: "stacks/a", Workspace: "blue",
				Wave: 2, Status: v1.StackPlanned, Reasons: []v1.Reason{v1.ReasonChanged}, Environment: "prod",
				Summary: summary, ExitCode: &exit, JobURL: "j", PlanArtifact: "a", PlanText: "t",
				PlanTextTruncated: true, FinishedAt: &testTime,
			}.ToV1(),
			want: v1.RunStack{
				StackID: testID.String(), Key: "stacks/a:blue", Path: "stacks/a", Workspace: "blue",
				Wave: 2, Status: v1.StackPlanned, Reasons: []v1.Reason{v1.ReasonChanged}, Environment: "prod",
				Summary: summary, ExitCode: &exit, JobURL: "j", PlanArtifact: "a", PlanText: "t",
				Truncated: true, FinishedAt: ptrTo(testTime.UTC()),
			},
		},
		{
			name: "run stack without reasons",
			got:  RunStack{StackID: testID, Reasons: []v1.Reason{}}.ToV1(),
			want: v1.RunStack{StackID: testID.String()},
		},
		{
			name: "stack run reference",
			got: StackRun{
				RunStack: RunStack{RunID: testOther, Status: v1.StackApplied, Summary: summary, JobURL: "j", FinishedAt: &testTime},
				SHA:      "s", PRNumber: 3,
			}.ToV1(),
			want: v1.RunStackRef{RunID: testOther.String(), SHA: "s", PRNumber: 3, Status: v1.StackApplied, Summary: summary, JobURL: "j", FinishedAt: ptrTo(testTime.UTC())},
		},
		{
			name: "stack",
			got:  Stack{ID: testID, Repo: "acme/infra", Key: "k", Path: "k", Backend: backend, Environment: "e", Tool: v1.ToolTofu}.ToV1(),
			want: v1.Stack{Key: "k", Path: "k", Repo: "acme/infra", Backend: backend, Environment: "e", Tool: v1.ToolTofu},
		},
		{
			name: "stack detail",
			got:  Stack{ID: testID, Repo: "acme/infra", Key: "k", Path: "k", Backend: backend, Tool: v1.ToolTerraform}.Detail(),
			want: v1.StackDetail{ID: testID.String(), Repo: "acme/infra", Key: "k", Path: "k", Backend: backend, Tool: v1.ToolTerraform},
		},
		{
			name: "lock",
			got:  Lock{StackID: testID, StackKey: "k", RunID: testOther, PRNumber: 5, TakenAt: testTime, Reason: "apply"}.ToV1(),
			want: v1.LockInfo{StackID: testID.String(), StackKey: "k", RunID: testOther.String(), PRNumber: 5, TakenAt: testTime.UTC(), Reason: "apply"},
		},
		{
			name: "check",
			got:  Check{Name: "policy", Status: v1.CheckWarn, Summary: "s", Details: "d", DetailsURL: "u", UpdatedAt: testTime}.ToV1(),
			want: v1.Check{Name: "policy", Status: v1.CheckWarn, Summary: "s", DetailsURL: "u", UpdatedAt: testTime.UTC()},
		},
		{
			name: "drift",
			got:  Drift{CheckedAt: testTime, Drifted: true, Summary: summary, IssueNumber: 9}.ToV1(),
			want: v1.DriftStatus{CheckedAt: testTime.UTC(), Drifted: true, Summary: summary, IssueNumber: 9},
		},
		{
			name: "module",
			got:  Module{Key: "acme/m//vpc@v1", BaseKey: "acme/m//vpc", Kind: v1.ModuleGit, Source: "git::x", Ref: "v1"}.ToV1(),
			want: v1.Module{Key: "acme/m//vpc@v1", Kind: v1.ModuleGit, Source: "git::x", Ref: "v1"},
		},
		{
			name: "module version",
			got:  ModuleVersion{Version: "v1", SHA: "s", TaggedAt: testTime}.ToV1(),
			want: v1.ModuleVersion{Version: "v1", SHA: "s", TaggedAt: testTime.UTC()},
		},
		{
			name: "module consumer",
			got:  ModuleConsumer{StackID: testID, Repo: "r", StackKey: "k", ModuleKey: "m", Ref: "v1", Latest: "v3", Behind: 2}.ToV1(),
			want: v1.ModuleConsumer{StackID: testID.String(), Repo: "r", StackKey: "k", Ref: "v1", Behind: 2},
		},
		{
			name: "module use",
			got:  ModuleConsumer{ModuleKey: "m", Ref: "v1", Latest: "v3", Behind: 2}.Consume(),
			want: v1.ModuleConsume{ModuleKey: "m", Ref: "v1", Latest: "v3", Behind: 2},
		},
		{
			name: "session",
			got:  Session{Login: "octocat", AvatarURL: "a"}.ToV1(),
			want: v1.Whoami{Login: "octocat", AvatarURL: "a", Orgs: []string{}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.got)
		})
	}
	assert.True(t, Module{Key: "a", BaseKey: "a"}.Family())
	assert.False(t, Module{Key: "a@v1", BaseKey: "a"}.Family())
}

func TestHashToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, hashToken(tc.in), tc.in)
	}
	a, err := randomToken()
	require.NoError(t, err)
	b, err := randomToken()
	require.NoError(t, err)
	assert.Len(t, a, 43)
	assert.NotEqual(t, a, b)
	assert.NotEqual(t, a, hashToken(a))
}

func TestCursor(t *testing.T) {
	enc := encodeCursor(testTime, testID)
	c, err := decodeCursor("op", enc)
	require.NoError(t, err)
	assert.True(t, c.at.Equal(testTime))
	assert.Equal(t, testID, c.id)
	at, id := c.args()
	assert.True(t, at.Equal(testTime))
	assert.Equal(t, testID, *id)

	empty, err := decodeCursor("op", "")
	require.NoError(t, err)
	assert.Nil(t, empty)
	at, id = empty.args()
	assert.Nil(t, at)
	assert.Nil(t, id)

	bad := []string{"***", b64("no-separator"), b64("yesterday|" + testID.String()), b64(testTime.Format(time.RFC3339Nano) + "|nope")}
	for _, in := range bad {
		_, err := decodeCursor("op", in)
		require.ErrorIs(t, err, ErrInvalid, in)
	}

	n, err := decodeIDCursor("op", encodeIDCursor(42))
	require.NoError(t, err)
	assert.Equal(t, int64(42), n)
	for _, in := range []string{"***", b64("x"), b64("-1"), b64("0")} {
		_, err := decodeIDCursor("op", in)
		require.ErrorIs(t, err, ErrInvalid, in)
	}
}

func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestPageSize(t *testing.T) {
	cases := map[int]int{-1: defaultPageSize, 0: defaultPageSize, 10: 10, maxPageSize: maxPageSize, maxPageSize + 1: maxPageSize}
	for in, want := range cases {
		assert.Equal(t, want, pageSize(in), in)
	}
}

func TestSplitModuleKey(t *testing.T) {
	cases := []struct {
		name     string
		in       v1.Module
		wantBase string
		wantRef  string
	}{
		{"local", v1.Module{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal}, "acme/infra//modules/vpc", ""},
		{"git with ref", v1.Module{Key: "acme/m//vpc@v1.2.0", Kind: v1.ModuleGit, Ref: "v1.2.0"}, "acme/m//vpc", "v1.2.0"},
		{"git ref with slash", v1.Module{Key: "acme/m//vpc@feature/x", Kind: v1.ModuleGit, Ref: "feature/x"}, "acme/m//vpc", "feature/x"},
		{"git ref from key", v1.Module{Key: "gitlab.com/acme/m//vpc@v2", Kind: v1.ModuleGit}, "gitlab.com/acme/m//vpc", "v2"},
		{"registry", v1.Module{Key: "registry:ns/vpc/aws@5.1", Kind: v1.ModuleRegistry}, "registry:ns/vpc/aws", "5.1"},
		{"unpinned git", v1.Module{Key: "acme/m//vpc", Kind: v1.ModuleGit}, "acme/m//vpc", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, ref := splitModuleKey(tc.in)
			assert.Equal(t, tc.wantBase, base)
			assert.Equal(t, tc.wantRef, ref)
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		limit   int
		want    string
		wantCut bool
	}{
		{"short", "abc", 5, "abc", false},
		{"exact", "abcde", 5, "abcde", false},
		{"ascii", "abcdef", 4, "abcd", true},
		{"inside a rune", "aé", 2, "a", true},
		{"after a rune", "aéb", 3, "aé", true},
		{"zero", "abc", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cut := truncateUTF8(tc.in, tc.limit)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantCut, cut)
		})
	}
}

func TestSplitStacks(t *testing.T) {
	cases := []struct {
		name         string
		in           []v1.Stack
		wantLocal    []string
		wantExternal []string
		wantErr      error
	}{
		{
			name: "local sorted, external kept apart",
			in: []v1.Stack{
				{Key: "b", Path: "b"},
				{Path: "a", Workspace: "blue"},
				{Key: "tgw", Path: "tgw", External: true},
				{Key: "dns", Path: "dns", Repo: "acme/network"},
				{Key: "c", Path: "c", Repo: "ACME/Infra"},
			},
			wantLocal:    []string{"a:blue", "b", "c"},
			wantExternal: []string{"tgw", "dns"},
		},
		{name: "duplicate", in: []v1.Stack{{Key: "a"}, {Key: "a"}}, wantErr: ErrInvalid},
		{name: "no key", in: []v1.Stack{{}}, wantErr: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local, external, err := splitStacks("acme/infra", tc.in)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantLocal, keysOf(local))
			assert.Equal(t, tc.wantExternal, keysOf(external))
		})
	}
}

func keysOf(stacks []v1.Stack) []string {
	out := make([]string, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, s.Key)
	}
	return out
}

func TestWrap(t *testing.T) {
	cause := errors.New("boom")
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"no rows", pgx.ErrNoRows, ErrNotFound},
		{"foreign key", &pgconn.PgError{Code: "23503"}, ErrNotFound},
		{"unique", &pgconn.PgError{Code: "23505"}, ErrConflict},
		{"check", &pgconn.PgError{Code: "23514"}, ErrInvalid},
		{"bad text", fmt.Errorf("encode: %w", &pgconn.PgError{Code: "22P02"}), ErrInvalid},
		{"bad json escape", &pgconn.PgError{Code: "22P05"}, ErrInvalid},
		{"other", cause, cause},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := wrap("op", tc.in)
			require.ErrorIs(t, err, tc.want)
			assert.True(t, strings.HasPrefix(err.Error(), "store: op: "), err.Error())
		})
	}
	require.NoError(t, wrap("op", nil))
}

func TestStatusSets(t *testing.T) {
	assert.ElementsMatch(t, []string{"applied", "failed", "unconfirmed", "superseded"}, terminalRunStatuses)
	assert.ElementsMatch(t, []string{"planned", "applied", "failed", "unconfirmed", "superseded"}, finishedRunStatuses)
	assert.ElementsMatch(t, []string{"planned", "applied", "failed", "blocked", "noop", "unconfirmed", "unknown", "skipped"}, finishedStackStates)
	assert.Equal(t, []string{"planning", "applying"}, startedStatuses)
}

func TestSmallHelpers(t *testing.T) {
	assert.Equal(t, []string{"acme", "globex"}, lowerAll([]string{"ACME", "", "Globex"}))
	assert.Empty(t, lowerAll(nil))
	assert.Equal(t, "{}", jsonPayload(nil))
	assert.JSONEq(t, `{"a":1}`, jsonPayload([]byte(`{"a":1}`)))
	assert.Empty(t, errorText(nil))
	assert.Len(t, errorText(errors.New(strings.Repeat("x", maxErrorText+10))), maxErrorText)
	assert.Equal(t, int64(1500000), micros(1500*time.Millisecond))
	assert.NotNil(t, nonNil[string](nil))
	assert.Nil(t, utc(nil))
	assert.Equal(t, time.UTC, utc(&testTime).Location())

	text, err := jsonText[v1.Backend](nil)
	require.NoError(t, err)
	assert.Nil(t, text)
	text, err = jsonText(&v1.Backend{Type: "s3"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"s3"}`, *text)
}

func ptrTo[T any](v T) *T { return &v }
