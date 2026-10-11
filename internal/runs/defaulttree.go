package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *Service) defaultDir(ctx context.Context, c *gh.Client, repo store.Repo, dir string) ([]gh.DirEntry, bool, error) {
	ref, ttl := defaultRef(repo)
	key := "stackdir:" + strconv.FormatInt(repo.ID, 10) + ":" + ref + ":" + dir
	if v, ok := s.cache.get(key, ttl, s.now()); ok {
		data, found := strings.CutPrefix(v, stackFileFound)
		if !found {
			return nil, false, nil
		}
		var entries []gh.DirEntry
		if json.Unmarshal([]byte(data), &entries) == nil {
			return entries, true, nil
		}
	}
	entries, err := c.ListDir(ctx, repo.FullName, dir, ref)
	switch {
	case errors.Is(err, gh.ErrNotFound):
		s.cache.put(key, stackFileMissing, s.now())
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("runs: %s of %s on %s: %w", dir, repo.FullName, ref, err)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, false, fmt.Errorf("runs: %s of %s on %s: %w", dir, repo.FullName, ref, err)
	}
	s.cache.put(key, stackFileFound+string(data), s.now())
	return entries, true, nil
}

type defaultTree struct {
	ctx  context.Context
	s    *Service
	c    *gh.Client
	repo store.Repo
	dir  string
	err  error
}

func (t *defaultTree) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: errors.ErrUnsupported}
}

func (t *defaultTree) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	listing, found, err := t.s.defaultDir(t.ctx, t.c, t.repo, path.Join(t.dir, name))
	if err != nil {
		if t.err == nil {
			t.err = err
		}
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if !found {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	entries := make([]fs.DirEntry, 0, len(listing))
	for _, e := range listing {
		entries = append(entries, treeEntry{name: e.Name, dir: e.Type == "dir"})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (t *defaultTree) Stat(name string) (fs.FileInfo, error) {
	if name == "." {
		return treeEntry{name: ".", dir: true}, nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	entries, err := t.ReadDir(path.Dir(name))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == path.Base(name) {
			return e.Info()
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

type treeEntry struct {
	name string
	dir  bool
}

func (e treeEntry) Name() string               { return e.name }
func (e treeEntry) IsDir() bool                { return e.dir }
func (e treeEntry) Type() fs.FileMode          { return e.Mode().Type() }
func (e treeEntry) Info() (fs.FileInfo, error) { return e, nil }
func (e treeEntry) Size() int64                { return 0 }
func (e treeEntry) ModTime() time.Time         { return time.Time{} }
func (e treeEntry) Sys() any                   { return nil }

func (e treeEntry) Mode() fs.FileMode {
	if e.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
