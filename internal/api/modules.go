package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func moduleOwner(key string) string {
	base, _, ok := strings.Cut(key, "//")
	if !ok {
		return ""
	}
	owner, repo, ok := strings.Cut(base, "/")
	if !ok || owner == "" || repo == "" || strings.ContainsAny(owner, ".:") || strings.Contains(repo, "/") {
		return ""
	}
	return owner
}

func repoOwner(fullName string) string {
	owner, _, _ := strings.Cut(fullName, "/")
	return owner
}

func familySource(fam store.Module, pinned []store.Module) string {
	if fam.Source != "" || len(pinned) == 0 {
		return fam.Source
	}
	best := pinned[0]
	for _, m := range pinned[1:] {
		bv, bok := parseSemver(best.Ref)
		mv, mok := parseSemver(m.Ref)
		switch {
		case mok && bok && mv.compare(bv) > 0, mok && !bok, !mok && !bok && m.Ref > best.Ref:
			best = m
		}
	}
	return best.Source
}

func (s *server) moduleDetail(ctx context.Context, id identity, fam store.Module, pinned []store.Module) (v1.ModuleDetail, bool, error) {
	versions, err := s.db.ListModuleVersions(ctx, fam.ID)
	if err != nil {
		return v1.ModuleDetail{}, false, fmt.Errorf("versions of module %s: %w", fam.BaseKey, err)
	}
	consumers, err := s.db.ModuleConsumers(ctx, fam.ID)
	if err != nil {
		return v1.ModuleDetail{}, false, fmt.Errorf("consumers of module %s: %w", fam.BaseKey, err)
	}
	d := v1.ModuleDetail{
		ID:     fam.ID.String(),
		Key:    fam.BaseKey,
		Kind:   fam.Kind,
		Source: familySource(fam, pinned),
	}
	d.Latest, _ = versionLag(versions, "")
	for _, v := range versions {
		d.Versions = append(d.Versions, v.ToV1())
	}
	for _, c := range consumers {
		if !id.sees(repoOwner(c.Repo)) {
			continue
		}
		out := c.ToV1()
		_, out.Behind = versionLag(versions, c.Ref)
		d.Consumers = append(d.Consumers, out)
	}
	slices.SortFunc(d.Consumers, func(a, b v1.ModuleConsumer) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.StackKey, b.StackKey), cmp.Compare(a.Ref, b.Ref), cmp.Compare(a.StackID, b.StackID))
	})
	d.Consumers = slices.Compact(d.Consumers)
	visible := id.sees(moduleOwner(fam.BaseKey)) || len(d.Consumers) > 0
	return d, visible, nil
}

func (s *server) modules(w http.ResponseWriter, r *http.Request, id identity) error {
	ctx := r.Context()
	limit, cursor, err := pageParams(r)
	if err != nil {
		return err
	}
	after, err := decodeKeyCursor(cursor)
	if err != nil {
		return err
	}
	all, err := s.db.ListModules(ctx, store.ModuleFilter{})
	if err != nil {
		return fmt.Errorf("list modules: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	pinned := map[string][]store.Module{}
	var families []store.Module
	for _, m := range all {
		if !m.Family() {
			pinned[m.BaseKey] = append(pinned[m.BaseKey], m)
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(m.BaseKey), query) {
			families = append(families, m)
		}
	}
	slices.SortFunc(families, func(a, b store.Module) int { return cmp.Compare(a.BaseKey, b.BaseKey) })

	page := v1.Page[v1.ModuleDetail]{Items: []v1.ModuleDetail{}}
	if id.Kind == principal.APIKey {
		page.Total = len(families)
	}
	for _, fam := range families {
		if fam.BaseKey <= after {
			continue
		}
		d, visible, err := s.moduleDetail(ctx, id, fam, pinned[fam.BaseKey])
		if err != nil {
			return err
		}
		if !visible {
			continue
		}
		if len(page.Items) == limit {
			page.NextCursor = encodeKeyCursor(page.Items[limit-1].Key)
			break
		}
		page.Items = append(page.Items, d)
	}
	s.writeJSON(w, r, http.StatusOK, page)
	return nil
}

func (s *server) module(w http.ResponseWriter, r *http.Request, id identity) error {
	ctx := r.Context()
	moduleID, err := parseID(r.PathValue("id"), "module")
	if err != nil {
		return err
	}
	missing := notFound("module " + r.PathValue("id") + " not found")
	m, err := s.db.GetModule(ctx, moduleID)
	if errors.Is(err, store.ErrNotFound) {
		return missing
	}
	if err != nil {
		return fmt.Errorf("get module: %w", err)
	}
	fam := m
	if !m.Family() {
		if fam, err = s.db.GetModuleByKey(ctx, m.BaseKey); err != nil {
			return fmt.Errorf("get module family %s: %w", m.BaseKey, err)
		}
	}
	var pinned []store.Module
	if fam.Kind != v1.ModuleLocal {
		if pinned, err = s.db.ListModules(ctx, store.ModuleFilter{Prefix: fam.BaseKey + "@"}); err != nil {
			return fmt.Errorf("list pinned modules: %w", err)
		}
	}
	d, visible, err := s.moduleDetail(ctx, id, fam, pinned)
	if err != nil {
		return err
	}
	if !visible {
		return missing
	}
	s.writeJSON(w, r, http.StatusOK, d)
	return nil
}
