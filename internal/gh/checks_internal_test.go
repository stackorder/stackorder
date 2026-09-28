package gh

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncateUTF8(t *testing.T) {
	invalidEarly := "\xff" + strings.Repeat("a", 100)
	invalidMiddle := strings.Repeat("é", 16000) + "\xff" + strings.Repeat("é", 100000)
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short input unchanged", "abc", 5, "abc"},
		{"exact length unchanged", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 4, "abcd"},
		{"two byte rune not split", strings.Repeat("é", 10), 5, "éé"},
		{"two byte rune at boundary", strings.Repeat("é", 10), 4, "éé"},
		{"four byte rune not split", "𝄞𝄞", 7, "𝄞"},
		{"four byte rune cut after its first byte", "𝄞𝄞", 5, "𝄞"},
		{"zero length", "é", 0, ""},
		{"invalid byte before the cut is kept", invalidEarly, 50, invalidEarly[:50]},
		{"invalid byte mid input keeps the rest", invalidMiddle, MaxCheckOutput, invalidMiddle[:MaxCheckOutput]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateUTF8(tt.in, tt.n)
			assert.Equal(t, len(tt.want), len(got))
			assert.True(t, got == tt.want, "truncated text differs from the expected prefix")
		})
	}
}
