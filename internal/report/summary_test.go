package report

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestSummaryLine(t *testing.T) {
	tests := []struct {
		name  string
		s     v1.PlanSummary
		short string
		long  string
	}{
		{"empty", v1.PlanSummary{}, "+0 ~0 -0", "No changes"},
		{"basic", v1.PlanSummary{Adds: 3, Changes: 1}, "+3 ~1 -0", "3 to add, 1 to change, 0 to destroy"},
		{"replace", v1.PlanSummary{Adds: 1, Destroys: 2, Replaces: 2}, "+1 ~0 -2 ±2", "1 to add, 0 to change, 2 to destroy, 2 to replace"},
		{"imports and moves", v1.PlanSummary{Imports: 2, Moves: 1}, "+0 ~0 -0", "0 to add, 0 to change, 0 to destroy, 2 to import, 1 to move"},
		{"outputs only", v1.PlanSummary{OutputChanges: 1}, "+0 ~0 -0", "No resource changes, 1 output to change"},
		{"outputs plural", v1.PlanSummary{OutputChanges: 3}, "+0 ~0 -0", "No resource changes, 3 outputs to change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.short, SummaryLine(tt.s))
			assert.Equal(t, tt.long, SummaryLong(tt.s))
		})
	}
	assert.Equal(t, "No resource changes, 2 outputs changed", appliedLong(v1.PlanSummary{OutputChanges: 2}))
	assert.Equal(t, "1 added, 0 changed, 0 destroyed, 1 replaced", appliedLong(v1.PlanSummary{Adds: 1, Replaces: 1}))
}
