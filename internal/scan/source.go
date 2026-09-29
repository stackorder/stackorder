package scan

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const githubHost = "github.com"

var (
	forcedGetterPrefix = regexp.MustCompile(`^([A-Za-z0-9]+)::(.+)$`)
	scpLikeGit         = regexp.MustCompile(`^(?:([^@/:]+)@)?([^@/:]+\.[^@/:]+):(.+)$`)
	registryName       = regexp.MustCompile(`^[0-9A-Za-z](?:[0-9A-Za-z_-]{0,62}[0-9A-Za-z])?$`)
	registryProvider   = regexp.MustCompile(`^[0-9a-z]{1,64}$`)
	registryHostname   = regexp.MustCompile(`^(?:(?:[A-Za-z0-9-]+\.)+[A-Za-z0-9-]+|localhost)(?::[0-9]+)?$`)

	shorthandGitHosts = map[string]bool{githubHost: true, "bitbucket.org": true}
	defaultRegistries = map[string]bool{"registry.terraform.io": true, "registry.opentofu.org": true}
	localSourcePrefix = []string{"./", "../", ".\\", "..\\"}
)

// ParseModuleSource normalises the source and version of a module block into
// a graph module node. It reports false for sources Stackorder does not track
// (s3::, gcs::, hg::, plain HTTP archives and anything unrecognised).
//
// Local sources ("./", "../") return Kind ModuleLocal, Path set to the cleaned
// source relative to the calling directory and an empty Key, because their
// identity depends on the caller; Scan resolves them to "owner/repo//path".
// Git sources return "owner/repo//subdir@ref" for github.com and keep the host
// for other servers ("gitlab.com/owner/repo//subdir@ref"); the subdir and
// "@ref" parts are omitted when absent and version is ignored. Registry sources
// return "registry:namespace/name/provider@version", keeping the host only
// for private registries. Source always holds the raw source as written.
func ParseModuleSource(source, version string) (v1.Module, bool) {
	return parseModuleSource(source, version, githubHost)
}

// parseModuleSource is ParseModuleSource with git sources on ghHost, the
// lowercased host of the GitHub instance, keyed without their host.
func parseModuleSource(source, version, ghHost string) (v1.Module, bool) {
	src := strings.TrimSpace(source)
	var (
		m  v1.Module
		ok bool
	)
	getter, rest := forcedGetter(src)
	switch {
	case src == "":
	case isLocalSource(src):
		m = v1.Module{Kind: v1.ModuleLocal, Path: path.Clean(strings.ReplaceAll(src, "\\", "/"))}
		ok = true
	case getter == "git":
		m, ok = parseGitSource(rest, true, ghHost)
	case getter != "":
	default:
		if m, ok = parseRegistrySource(src, strings.TrimSpace(version)); !ok {
			m, ok = parseGitSource(src, false, ghHost)
		}
	}
	if !ok {
		return v1.Module{}, false
	}
	m.Source = source
	return m, true
}

func gitHubHost(webURL string) (string, error) {
	if strings.TrimSpace(webURL) == "" {
		return githubHost, nil
	}
	u, err := url.Parse(strings.TrimSpace(webURL))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("GitHub URL %q has no host", webURL)
	}
	return strings.ToLower(u.Hostname()), nil
}

func forcedGetter(src string) (getter, rest string) {
	if m := forcedGetterPrefix.FindStringSubmatch(src); m != nil {
		return strings.ToLower(m[1]), m[2]
	}
	return "", src
}

func isLocalSource(src string) bool {
	for _, p := range localSourcePrefix {
		if strings.HasPrefix(src, p) {
			return true
		}
	}
	return false
}

func parseGitSource(src string, forced bool, ghHost string) (v1.Module, bool) {
	base, query, _ := strings.Cut(src, "?")
	repo, subdir := splitSubdir(base)
	host, repoPath, ok := splitGitLocation(repo, forced)
	if !ok {
		return v1.Module{}, false
	}
	repoPath, extra := splitRepoPath(repoPath, host == ghHost || shorthandGitHosts[host])
	if repoPath == "" {
		return v1.Module{}, false
	}
	key := repoPath
	if host != ghHost {
		key = host + "/" + repoPath
	}
	if subdir = cleanSubdir(extra + "/" + subdir); subdir != "" {
		key += "//" + subdir
	}
	ref := ""
	if values, err := url.ParseQuery(query); err == nil {
		ref = values.Get("ref")
	}
	if ref != "" {
		key += "@" + ref
	}
	return v1.Module{Key: key, Kind: v1.ModuleGit, Ref: ref}, true
}

func splitSubdir(src string) (base, subdir string) {
	offset := 0
	if i := strings.Index(src, "://"); i >= 0 {
		offset = i + len("://")
	}
	i := strings.Index(src[offset:], "//")
	if i < 0 {
		return src, ""
	}
	return src[:offset+i], src[offset+i+2:]
}

func splitGitLocation(repo string, forced bool) (host, repoPath string, ok bool) {
	if strings.Contains(repo, "://") {
		u, err := url.Parse(repo)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		switch strings.ToLower(u.Scheme) {
		case "ssh", "git":
		case "https", "http":
			if !forced {
				return "", "", false
			}
		default:
			return "", "", false
		}
		return strings.ToLower(u.Hostname()), strings.Trim(u.Path, "/"), true
	}
	if m := scpLikeGit.FindStringSubmatch(repo); m != nil {
		if !forced && m[1] != "git" {
			return "", "", false
		}
		return strings.ToLower(m[2]), strings.Trim(m[3], "/"), true
	}
	first, rest, found := strings.Cut(repo, "/")
	if !found || !strings.Contains(first, ".") {
		return "", "", false
	}
	host = strings.ToLower(first)
	if !forced && !shorthandGitHosts[host] {
		return "", "", false
	}
	return host, strings.Trim(rest, "/"), true
}

func splitRepoPath(repoPath string, ownerRepo bool) (repo, extra string) {
	segments := strings.Split(repoPath, "/")
	if ownerRepo {
		if len(segments) < 2 || segments[0] == "" || strings.TrimSuffix(segments[1], ".git") == "" {
			return "", ""
		}
		return segments[0] + "/" + strings.TrimSuffix(segments[1], ".git"), strings.Join(segments[2:], "/")
	}
	for i, s := range segments {
		if strings.HasSuffix(s, ".git") {
			segments[i] = strings.TrimSuffix(s, ".git")
			return strings.Join(segments[:i+1], "/"), strings.Join(segments[i+1:], "/")
		}
	}
	return repoPath, ""
}

func cleanSubdir(subdir string) string {
	return strings.Trim(path.Clean("/"+subdir), "/")
}

func parseRegistrySource(src, version string) (v1.Module, bool) {
	if strings.Contains(src, "?") {
		return v1.Module{}, false
	}
	base, subdir := splitSubdir(src)
	parts := strings.Split(base, "/")
	host := ""
	switch len(parts) {
	case 3:
	case 4:
		host = strings.ToLower(parts[0])
		if !registryHostname.MatchString(host) || shorthandGitHosts[host] {
			return v1.Module{}, false
		}
		parts = parts[1:]
	default:
		return v1.Module{}, false
	}
	if !registryName.MatchString(parts[0]) || !registryName.MatchString(parts[1]) || !registryProvider.MatchString(parts[2]) {
		return v1.Module{}, false
	}
	key := "registry:"
	if host != "" && !defaultRegistries[host] {
		key += host + "/"
	}
	key += strings.Join(parts, "/")
	if subdir = cleanSubdir(subdir); subdir != "" {
		key += "//" + subdir
	}
	if version != "" {
		key += "@" + version
	}
	return v1.Module{Key: key, Kind: v1.ModuleRegistry, Ref: version}, true
}
