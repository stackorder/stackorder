package stateread

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// AnyRepository is the Repository of a grant that covers every repository
// the server knows, an operator's explicit choice to let any of them have
// the server read the objects it covers.
const AnyRepository = "*"

const grantForms = "write owner/repo=bucket or owner/repo=bucket/prefix, or *=bucket[/prefix] for every repository"

var validRepository = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`).MatchString

// Grant lets the stacks of one repository, or of every repository when
// Repository is AnyRepository, have the server read the objects of one
// bucket whose keys lie under Prefix; an empty Prefix covers the whole
// bucket. The repository is the producer whose outputs are served, so a
// repository can never make the server read an object granted only to
// another.
type Grant struct {
	Repository string
	Bucket     string
	Prefix     string
}

// ParseGrants parses a comma-separated list of "owner/repo=bucket" and
// "owner/repo=bucket/prefix" entries, where "*" in place of owner/repo
// grants the location to every repository. Spaces around entries and
// around their two parts are ignored, and a blank list yields no grants.
// An entry without a repository, with a repository that is neither
// owner/repo nor "*", without a valid S3 bucket name, or with a "." or ".."
// segment in its prefix is an error.
func ParseGrants(s string) ([]Grant, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	entries := strings.Split(s, ",")
	grants := make([]Grant, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		repo, location, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("stateread: grant %q names no repository; %s", entry, grantForms)
		}
		bucket, prefix, _ := strings.Cut(strings.TrimSpace(location), "/")
		g := Grant{Repository: strings.TrimSpace(repo), Bucket: bucket, Prefix: prefix}
		if g.Bucket == "" {
			return nil, fmt.Errorf("stateread: grant %q has no bucket; %s", entry, grantForms)
		}
		if err := g.check(); err != nil {
			return nil, fmt.Errorf("stateread: grant %q: %w", entry, err)
		}
		grants = append(grants, g)
	}
	return grants, nil
}

func (g Grant) check() error {
	switch {
	case g.Repository != AnyRepository && !validRepository(g.Repository):
		return fmt.Errorf("repository %q is neither owner/repo nor %s; %s", g.Repository, AnyRepository, grantForms)
	case !validBucket(g.Bucket):
		return fmt.Errorf("%q is not a valid S3 bucket name", g.Bucket)
	case hasDotSegment(g.Prefix):
		return errors.New("a prefix with a . or .. segment covers no key the server reads")
	}
	return nil
}

// String returns the grant as ParseGrants reads it: "repository=bucket",
// or "repository=bucket/prefix" when it has a prefix.
func (g Grant) String() string {
	if g.Prefix == "" {
		return g.Repository + "=" + g.Bucket
	}
	return g.Repository + "=" + g.Bucket + "/" + g.Prefix
}

// Allows reports whether the grant lets the stacks of repo, compared
// without regard to case, have the server read loc. The grant must name
// repo or AnyRepository and loc's bucket, and loc's key must be the prefix
// or lie under it at a path segment boundary, where the prefix ends with
// "/" or the key goes on with "/" after it: "stacks/prod" covers
// "stacks/prod" and "stacks/prod/vpc.tfstate" but not
// "stacks/production/vpc.tfstate". A key with a "." or ".." path segment is
// never allowed, since a proxy could resolve it to a key outside the
// prefix.
func (g Grant) Allows(repo string, loc Location) bool {
	return repo != "" && (g.Repository == AnyRepository || strings.EqualFold(g.Repository, repo)) &&
		loc.Bucket == g.Bucket && loc.Key != "" && underPrefix(loc.Key, g.Prefix) && !hasDotSegment(loc.Key)
}

// Overlaps reports whether some object lies under the locations of both g
// and o, whatever their repositories: they name the same bucket and one's
// prefix is the other's or lies under it at a path segment boundary. Two
// overlapping grants of different repositories let either repository have
// the server read the objects both cover.
func (g Grant) Overlaps(o Grant) bool {
	return g.Bucket == o.Bucket && (underPrefix(g.Prefix, o.Prefix) || underPrefix(o.Prefix, g.Prefix))
}

// Allowed reports whether any of grants allows repo to have loc read.
func Allowed(grants []Grant, repo string, loc Location) bool {
	return slices.ContainsFunc(grants, func(g Grant) bool { return g.Allows(repo, loc) })
}

func underPrefix(key, prefix string) bool {
	rest, ok := strings.CutPrefix(key, prefix)
	return ok && (prefix == "" || rest == "" || strings.HasSuffix(prefix, "/") || strings.HasPrefix(rest, "/"))
}

func hasDotSegment(key string) bool {
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

var (
	reservedBucketPrefixes = []string{"xn--", "sthree-", "amzn-s3-demo-"}
	reservedBucketSuffixes = []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"}
)

func validBucket(name string) bool {
	if len(name) < 3 || len(name) > 63 || !alnum(name[0]) || !alnum(name[len(name)-1]) {
		return false
	}
	for i := range len(name) {
		if c := name[i]; !alnum(c) && c != '-' && c != '.' {
			return false
		}
	}
	if strings.Contains(name, "..") {
		return false
	}
	if addr, err := netip.ParseAddr(name); err == nil && addr.Is4() {
		return false
	}
	for _, p := range reservedBucketPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	for _, s := range reservedBucketSuffixes {
		if strings.HasSuffix(name, s) {
			return false
		}
	}
	return true
}

func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}
