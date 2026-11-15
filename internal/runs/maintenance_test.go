package runs

import "testing"

func TestDegradedListing(t *testing.T) {
	cases := []struct {
		omitted, total int
		want           bool
	}{
		{0, 0, false},
		{0, 1, false},
		{1, 1, false},
		{1, 2, false},
		{2, 2, true},
		{1, 3, false},
		{2, 3, true},
		{3, 6, false},
	}
	for _, c := range cases {
		if got := degradedListing(c.omitted, c.total); got != c.want {
			t.Errorf("degradedListing(%d, %d) = %v, want %v", c.omitted, c.total, got, c.want)
		}
	}
}

func TestDegradedRepoListing(t *testing.T) {
	cases := []struct {
		omitted, total int
		want           bool
	}{
		{0, 0, false},
		{0, 1, false},
		{1, 1, true},
		{1, 2, false},
		{2, 2, true},
		{1, 3, false},
		{2, 3, true},
		{3, 3, true},
		{3, 6, false},
	}
	for _, c := range cases {
		if got := degradedRepoListing(c.omitted, c.total); got != c.want {
			t.Errorf("degradedRepoListing(%d, %d) = %v, want %v", c.omitted, c.total, got, c.want)
		}
	}
}
