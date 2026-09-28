package api

import (
	"cmp"
	"strconv"
	"strings"

	"github.com/stackorder/stackorder/internal/store"
)

type semver struct {
	major, minor, patch uint64
	pre                 []string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validIdentifiers(s[i+1:], false) {
			return semver{}, false
		}
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validIdentifiers(s[i+1:], true) {
			return semver{}, false
		}
		v.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := [3]*uint64{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		n, ok := numeric(p)
		if !ok {
			return semver{}, false
		}
		*nums[i] = n
	}
	return v, true
}

func numeric(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func validIdentifiers(s string, noLeadingZeros bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		allDigits := true
		for i := range len(id) {
			c := id[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				allDigits = false
			default:
				return false
			}
		}
		if noLeadingZeros && allDigits && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func (a semver) compare(b semver) int {
	if c := cmp.Or(cmp.Compare(a.major, b.major), cmp.Compare(a.minor, b.minor), cmp.Compare(a.patch, b.patch)); c != 0 {
		return c
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePre(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a.pre), len(b.pre))
}

func comparePre(a, b string) int {
	an, aNum := numeric(a)
	bn, bNum := numeric(b)
	switch {
	case aNum && bNum:
		return cmp.Compare(an, bn)
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}

func versionLag(versions []store.ModuleVersion, ref string) (string, int) {
	type parsed struct {
		name string
		v    semver
	}
	var stable, pre []parsed
	for _, mv := range versions {
		v, ok := parseSemver(mv.Version)
		if !ok {
			continue
		}
		if len(v.pre) == 0 {
			stable = append(stable, parsed{mv.Version, v})
		} else {
			pre = append(pre, parsed{mv.Version, v})
		}
	}
	newest := func(list []parsed) (parsed, bool) {
		var best parsed
		for i, p := range list {
			if i == 0 || p.v.compare(best.v) > 0 {
				best = p
			}
		}
		return best, len(list) > 0
	}
	latest := ""
	if best, ok := newest(stable); ok {
		latest = best.name
	} else if best, ok := newest(pre); ok {
		latest = best.name
	} else if len(versions) > 0 {
		latest = versions[0].Version
	}

	target, ok := parseSemver(ref)
	if !ok {
		return latest, 0
	}
	candidates := stable
	if len(target.pre) > 0 {
		candidates = append(append([]parsed{}, stable...), pre...)
	}
	var newer []semver
	for _, c := range candidates {
		if c.v.compare(target) <= 0 {
			continue
		}
		dup := false
		for _, n := range newer {
			if n.compare(c.v) == 0 {
				dup = true
				break
			}
		}
		if !dup {
			newer = append(newer, c.v)
		}
	}
	return latest, len(newer)
}
