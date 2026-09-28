package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stackorder/stackorder/internal/store"
)

func TestParseSemver(t *testing.T) {
	valid := map[string]semver{
		"1.2.3":                 {major: 1, minor: 2, patch: 3},
		"v1.2.3":                {major: 1, minor: 2, patch: 3},
		"V0.0.0":                {},
		"v10.20.30":             {major: 10, minor: 20, patch: 30},
		"1.0.0-rc.1":            {major: 1, pre: []string{"rc", "1"}},
		"1.0.0-alpha-1.x":       {major: 1, pre: []string{"alpha-1", "x"}},
		"1.0.0+build.5":         {major: 1},
		"1.0.0-beta+exp.sha.51": {major: 1, pre: []string{"beta"}},
		"1.0.0-0a":              {major: 1, pre: []string{"0a"}},
	}
	for in, want := range valid {
		got, ok := parseSemver(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "v", "1", "v1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.3+a_b", "main", "a1b2c3d", "-1.2.3", "1.2.x"} {
		_, ok := parseSemver(in)
		assert.False(t, ok, in)
	}
}

func TestSemverPrecedence(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			a, _ := parseSemver(ordered[i])
			b, _ := parseSemver(ordered[j])
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			assert.Equal(t, want, a.compare(b), "%s vs %s", ordered[i], ordered[j])
		}
	}
	a, _ := parseSemver("1.0.0+build.1")
	b, _ := parseSemver("v1.0.0+build.2")
	assert.Zero(t, a.compare(b), "build metadata is ignored")
}

func TestVersionLag(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 1, 1+n, 0, 0, 0, 0, time.UTC) }
	versions := []store.ModuleVersion{
		{Version: "v1.3.0", TaggedAt: day(3)},
		{Version: "v2.0.0-rc.1", TaggedAt: day(2)},
		{Version: "v1.4.1", TaggedAt: day(1)},
		{Version: "latest", TaggedAt: day(1)},
		{Version: "1.4.1", TaggedAt: day(1)},
		{Version: "v1.2.0", TaggedAt: day(0)},
	}
	cases := []struct {
		name   string
		in     []store.ModuleVersion
		ref    string
		latest string
		behind int
	}{
		{name: "behind by semver, not by tag time", in: versions, ref: "v1.2.0", latest: "v1.4.1", behind: 2},
		{name: "ref without v", in: versions, ref: "1.3.0", latest: "v1.4.1", behind: 1},
		{name: "up to date", in: versions, ref: "v1.4.1", latest: "v1.4.1", behind: 0},
		{name: "ahead of every release", in: versions, ref: "v3.0.0", latest: "v1.4.1", behind: 0},
		{name: "pre-release ref counts pre-releases", in: versions, ref: "v2.0.0-beta.1", latest: "v1.4.1", behind: 1},
		{name: "branch ref", in: versions, ref: "main", latest: "v1.4.1", behind: 0},
		{name: "moving major tag", in: versions, ref: "v1", latest: "v1.4.1", behind: 0},
		{name: "local module", ref: "", latest: "", behind: 0},
		{name: "only pre-releases", in: []store.ModuleVersion{{Version: "v1.0.0-rc.1"}, {Version: "v1.0.0-rc.2"}}, ref: "v1.0.0-rc.1", latest: "v1.0.0-rc.2", behind: 1},
		{name: "no semantic versions", in: []store.ModuleVersion{{Version: "release-b"}, {Version: "release-a"}}, ref: "release-a", latest: "release-b", behind: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			latest, behind := versionLag(tc.in, tc.ref)
			assert.Equal(t, tc.latest, latest)
			assert.Equal(t, tc.behind, behind)
		})
	}
}
