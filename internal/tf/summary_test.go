package tf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func loadPlan(t *testing.T, name string) *tfjson.Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	p, err := ParsePlanJSON(data)
	require.NoError(t, err)
	return p
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		fixture   string
		want      v1.PlanSummary
		wantAddrs []string
		wantEmpty bool
	}{
		{
			fixture: "create.json",
			want: v1.PlanSummary{
				Adds:          2,
				Added:         []string{"aws_s3_bucket.logs", "module.vpc.aws_vpc.this"},
				OutputChanges: 1,
			},
			wantAddrs: []string{"aws_s3_bucket.logs", "module.vpc.aws_vpc.this"},
		},
		{
			fixture: "update.json",
			want: v1.PlanSummary{
				Changes: 2,
				Changed: []string{"aws_instance.web[0]", "aws_security_group.web"},
			},
			wantAddrs: []string{"aws_instance.web[0]", "aws_security_group.web"},
		},
		{
			fixture: "delete.json",
			want: v1.PlanSummary{
				Destroys:      2,
				Destroyed:     []string{`aws_iam_user.u["bob"]`, "module.legacy.aws_sqs_queue.jobs"},
				OutputChanges: 1,
			},
			wantAddrs: []string{`aws_iam_user.u["bob"]`, "module.legacy.aws_sqs_queue.jobs"},
		},
		{
			fixture: "replace.json",
			want: v1.PlanSummary{
				Destroys:      1,
				Destroyed:     []string{"aws_launch_template.lt"},
				Replaces:      3,
				Replaced:      []string{"aws_instance.db", "aws_launch_template.lt", "random_id.suffix"},
				OutputChanges: 1,
			},
			wantAddrs: []string{"aws_instance.db", "aws_launch_template.lt", "random_id.suffix"},
		},
		{
			fixture: "import.json",
			want: v1.PlanSummary{
				Adds:    1,
				Added:   []string{"aws_s3_bucket_policy.legacy"},
				Changes: 1,
				Changed: []string{"aws_iam_role.deploy"},
				Imports: 2,
			},
			wantAddrs: []string{"aws_iam_role.deploy", "aws_s3_bucket_policy.legacy"},
		},
		{
			fixture: "move.json",
			want: v1.PlanSummary{
				Changes: 1,
				Changed: []string{"module.network.aws_vpc.main"},
				Moves:   1,
			},
			wantAddrs: []string{"module.network.aws_vpc.main"},
		},
		{
			fixture:   "noop.json",
			want:      v1.PlanSummary{},
			wantEmpty: true,
		},
		{
			fixture: "outputs.json",
			want:    v1.PlanSummary{OutputChanges: 3},
		},
		{
			fixture: "mixed.json",
			want: v1.PlanSummary{
				Adds:          2,
				Added:         []string{`aws_subnet.private["a"]`, `aws_subnet.private["b"]`},
				Changes:       1,
				Changed:       []string{"aws_db_instance.main"},
				Destroys:      1,
				Destroyed:     []string{"aws_nat_gateway.this[0]"},
				Replaces:      1,
				Replaced:      []string{"aws_eip.nat"},
				Imports:       1,
				Moves:         1,
				OutputChanges: 1,
			},
			wantAddrs: []string{
				"aws_db_instance.main", "aws_eip.nat", "aws_nat_gateway.this[0]",
				`aws_subnet.private["a"]`, `aws_subnet.private["b"]`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			p := loadPlan(t, tt.fixture)
			got := Summarize(p)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("Summarize mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, tt.wantEmpty, got.Empty())
			assert.Equal(t, tt.wantAddrs, AddressSet(p))
			assert.Equal(t, tt.wantAddrs, SummaryAddressSet(got))
			assert.Equal(t, got, Summarize(loadPlan(t, tt.fixture)), "Summarize must be deterministic")
		})
	}
}

func TestSummarizeNil(t *testing.T) {
	tests := []struct {
		name string
		plan *tfjson.Plan
	}{
		{name: "nil plan", plan: nil},
		{name: "nil entries", plan: &tfjson.Plan{
			ResourceChanges: []*tfjson.ResourceChange{nil, {Address: "a.b"}},
			OutputChanges:   map[string]*tfjson.Change{"x": nil, "y": {}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Summarize(tt.plan)
			assert.True(t, s.Empty())
			assert.Nil(t, AddressSet(tt.plan))
		})
	}
}

func TestSameAddressSet(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{name: "both empty", want: true},
		{name: "nil and empty", a: nil, b: []string{}, want: true},
		{name: "same order", a: []string{"a.x", "b.y"}, b: []string{"a.x", "b.y"}, want: true},
		{name: "different order", a: []string{"b.y", "a.x"}, b: []string{"a.x", "b.y"}, want: true},
		{name: "duplicates ignored", a: []string{"a.x", "a.x", "b.y"}, b: []string{"b.y", "a.x"}, want: true},
		{name: "extra address", a: []string{"a.x"}, b: []string{"a.x", "b.y"}, want: false},
		{name: "different address", a: []string{"a.x"}, b: []string{"a.y"}, want: false},
		{name: "one empty", a: []string{"a.x"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SameAddressSet(tt.a, tt.b))
			assert.Equal(t, tt.want, SameAddressSet(tt.b, tt.a))
		})
	}
}

func TestSameAddressSetDoesNotMutate(t *testing.T) {
	a := []string{"b.y", "a.x"}
	SameAddressSet(a, []string{"a.x", "b.y"})
	assert.Equal(t, []string{"b.y", "a.x"}, a)
}
