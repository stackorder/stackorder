package stateread

import (
	"fmt"
	"net/netip"
	"strings"
)

// Grant allows reads of the objects of one bucket whose keys start with
// Prefix; an empty Prefix allows the whole bucket.
type Grant struct {
	Bucket string
	Prefix string
}

// ParseGrants parses a comma-separated list of "bucket" and
// "bucket/prefix" entries. Spaces around entries are ignored, a blank list
// yields no grants, and an entry without a valid S3 bucket name is an
// error.
func ParseGrants(s string) ([]Grant, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	entries := strings.Split(s, ",")
	grants := make([]Grant, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		bucket, prefix, _ := strings.Cut(entry, "/")
		if bucket == "" {
			return nil, fmt.Errorf("stateread: grant %q has no bucket", entry)
		}
		if !validBucket(bucket) {
			return nil, fmt.Errorf("stateread: grant %q: %q is not a valid S3 bucket name", entry, bucket)
		}
		grants = append(grants, Grant{Bucket: bucket, Prefix: prefix})
	}
	return grants, nil
}

// Allows reports whether loc is an object the grant covers: the same
// bucket and a key starting with the prefix, compared as plain strings as
// IAM compares resource ARNs. A key with a "." or ".." path segment is
// never allowed, since a proxy could resolve it to a key outside the
// prefix.
func (g Grant) Allows(loc Location) bool {
	return loc.Bucket == g.Bucket && loc.Key != "" &&
		strings.HasPrefix(loc.Key, g.Prefix) && !hasDotSegment(loc.Key)
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
