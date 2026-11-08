package config

import (
	"net"
	"net/url"
	"strings"
)

// AWSS3 is what S3Service returns for an endpoint of AWS S3 itself.
const AWSS3 = "aws"

// awsHostSuffixes are the domains of AWS S3 endpoints: regional, FIPS,
// dual-stack, access point and VPC endpoint hosts, in the commercial,
// GovCloud and China partitions.
var awsHostSuffixes = []string{".amazonaws.com", ".amazonaws.com.cn", ".api.aws"}

// S3Endpoint returns the canonical spelling of the S3 endpoint URL that an
// S3 backend configures in endpoints.s3 or the deprecated endpoint, such as
// https://<account>.r2.cloudflarestorage.com for Cloudflare R2: white space
// trimmed, https:// added when there is no scheme, scheme and host in
// lower case, the scheme's default port and any trailing slash dropped, and
// no query, fragment or user information. An empty or blank endpoint
// yields "", and one that is not a URL is returned trimmed of white space
// and trailing slashes.
func S3Endpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return strings.TrimRight(endpoint, "/")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if h, port, err := net.SplitHostPort(host); err == nil &&
		(scheme == "https" && port == "443" || scheme == "http" && port == "80") {
		host = h
		if strings.Contains(h, ":") {
			host = "[" + h + "]"
		}
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

// S3Service names the S3 service an endpoint reaches, so that two spellings
// of one service compare equal: "" for an empty endpoint, which leaves the
// choice to the environment, AWSS3 for any endpoint of AWS S3, and the
// endpoint's S3Endpoint spelling for any other, S3-compatible, service.
func S3Service(endpoint string) string {
	endpoint = S3Endpoint(endpoint)
	if endpoint == "" {
		return ""
	}
	if u, err := url.Parse(endpoint); err == nil && isAWSHost(u.Hostname()) {
		return AWSS3
	}
	return endpoint
}

func isAWSHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	for _, suffix := range awsHostSuffixes {
		if host == suffix[1:] || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
