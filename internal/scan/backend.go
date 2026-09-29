package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

var errOutsideRepository = errors.New("outside the repository")

func (s *scanner) effectiveBackend(key string, base *v1.Backend, entries []string) (*v1.Backend, []string, error) {
	var be *v1.Backend
	if base != nil {
		c := *base
		be = &c
	}
	var files []string
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			if be != nil {
				s.setBackendValue(key, be, name, value)
			}
			continue
		}
		file, err := repoFile(entry)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: backend_config file %s: %w", key, entry, err)
		}
		src, err := s.fsys.ReadFile(filepath.FromSlash(file))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: backend_config file %s: %w", key, file, pathError(err))
		}
		files = append(files, file)
		s.recordInput(file, src)
		if be != nil {
			s.hcl.overlayBackendFile(file, src, be)
		}
	}
	return be, files, nil
}

func (s *scanner) setBackendValue(key string, be *v1.Backend, name, value string) {
	if name == "use_lockfile" {
		v, err := strconv.ParseBool(value)
		if err != nil {
			s.warn("%s: backend_config %s=%s is not a boolean; skipped", key, name, value)
			return
		}
		be.UseLockfile = v
		return
	}
	setS3String(be, name, value)
}

func (s *scanner) inputFiles(eff config.Effective, backendFiles []string) []string {
	out := slices.Clone(backendFiles)
	for _, vf := range eff.VarFiles {
		file, err := repoFile(path.Join(eff.Path, vf))
		if err != nil {
			s.warn("%s: var file %s is %v", eff.Key, vf, err)
			continue
		}
		if err := s.hashInput(file); errors.Is(err, fs.ErrNotExist) {
			s.warn("%s: var file %s does not exist", eff.Key, file)
		} else if err != nil {
			s.warn("%s: var file %s: %v", eff.Key, file, pathError(err))
		}
		out = append(out, file)
	}
	return out
}

func (s *scanner) setWatchPaths() {
	dirs := map[string]bool{}
	for _, info := range s.stacks {
		dirs[info.stack.Path] = true
	}
	for _, info := range s.stacks {
		var out []string
		for _, f := range info.inputs {
			if owner(dirs, f) != info.stack.Path {
				out = append(out, f)
			}
		}
		slices.Sort(out)
		info.stack.WatchPaths = slices.Compact(out)
	}
}

func owner(dirs map[string]bool, file string) string {
	for d := path.Dir(file); d != "."; d = path.Dir(d) {
		if dirs[d] {
			return d
		}
	}
	return ""
}

func repoFile(p string) (string, error) {
	c := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if c == "." || c == ".." || path.IsAbs(c) || strings.HasPrefix(c, "../") {
		return "", errOutsideRepository
	}
	return c, nil
}
