package report

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncatePlan(t *testing.T) {
	note := TruncationNote + "\n"
	tests := []struct {
		name     string
		text     string
		max      int
		want     string
		wantDrop bool
	}{
		{name: "fits", text: "a\nb\n", max: 4, want: "a\nb\n"},
		{name: "empty", text: "", max: 0, want: ""},
		{name: "cut at last full line", text: "line one\nline two\nline three\n" + strings.Repeat("more\n", 20), max: 18 + len(note), want: "line one\nline two\n" + note, wantDrop: true},
		{name: "partial line dropped", text: "line one\nline two\nline three\n" + strings.Repeat("more\n", 20), max: 12 + len(note), want: "line one\n" + note, wantDrop: true},
		{name: "single long line cut mid-line", text: strings.Repeat("x", 100), max: 10 + len(note), want: strings.Repeat("x", 9) + "\n" + note, wantDrop: true},
		{name: "multibyte cut on rune boundary", text: strings.Repeat("é", 50), max: 10 + len(note), want: strings.Repeat("é", 4) + "\n" + note, wantDrop: true},
		{name: "only the note fits", text: strings.Repeat("x", 100), max: len(note), want: note, wantDrop: true},
		{name: "note plus one byte", text: strings.Repeat("x", 100), max: len(note) + 1, want: note, wantDrop: true},
		{name: "note does not fit", text: strings.Repeat("x", 100), max: len(note) - 1, want: "", wantDrop: true},
		{name: "negative budget", text: "x", max: -5, want: "", wantDrop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := TruncatePlan(tt.text, tt.max)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantDrop, dropped)
			assert.LessOrEqual(t, len(got), max(tt.max, 0))
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func TestTruncatePlanNeverExceedsBudget(t *testing.T) {
	text := strings.Repeat("résumé ✓ line of plan text\n", 500) + strings.Repeat("ü", 3000)
	for limit := 0; limit < len(text)+10; limit += 97 {
		got, dropped := TruncatePlan(text, limit)
		require.LessOrEqual(t, len(got), limit, "limit %d", limit)
		require.True(t, utf8.ValidString(got), "limit %d", limit)
		require.Equal(t, len(text) > limit, dropped, "limit %d", limit)
		if dropped && got != "" {
			require.True(t, strings.HasSuffix(got, TruncationNote+"\n"), "limit %d", limit)
		}
	}
}

func TestShareBudget(t *testing.T) {
	tests := []struct {
		name   string
		sizes  []int
		budget int
		want   []int
	}{
		{name: "everything fits", sizes: []int{10, 20, 30}, budget: 100, want: []int{10, 20, 30}},
		{name: "even split", sizes: []int{100, 100, 100}, budget: 90, want: []int{30, 30, 30}},
		{name: "small ones donate", sizes: []int{500, 10, 500}, budget: 210, want: []int{100, 10, 100}},
		{name: "remainder goes to the largest", sizes: []int{50, 50, 50}, budget: 100, want: []int{33, 33, 34}},
		{name: "zero budget", sizes: []int{5, 5}, budget: 0, want: []int{0, 0}},
		{name: "negative budget", sizes: []int{5}, budget: -1, want: []int{0}},
		{name: "no sizes", sizes: nil, budget: 10, want: []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shareBudget(tt.sizes, tt.budget)
			assert.Equal(t, tt.want, got)
			total := 0
			for _, a := range got {
				total += a
			}
			assert.LessOrEqual(t, total, max(tt.budget, 0))
		})
	}
}
