package runs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	resourcesTimeout        = 10 * time.Second
	maxResourceAddressBytes = 2048
	maxResourceTypeBytes    = 512
	maxResourceTotal        = 10_000_000
)

type droppedResource struct {
	index  int
	field  string
	reason string
}

func (s *Service) recordResources(ctx context.Context, run store.Run, stack store.Stack, res v1.StackResult) {
	if res.Resources == nil || (run.Mode != v1.ModeApply && run.Mode != v1.ModeDrift) {
		return
	}
	list, dropped, first := cleanResources(*res.Resources)
	if dropped > 0 {
		s.log.WarnContext(ctx, "drop invalid resources", "run_id", run.ID, "stack", stack.Key,
			"dropped", dropped, "kept", len(list.Items), "total", list.Total,
			"index", first.index, "field", first.field, "reason", first.reason)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resourcesTimeout)
	defer cancel()
	if err := s.st.SetStackResources(ctx, stack.ID, run.ID, list, res.Unconfirmed); err != nil {
		s.log.WarnContext(ctx, "record resources", "run_id", run.ID, "stack", stack.Key, "error", err)
	}
}

func cleanResources(in v1.ResourceList) (v1.ResourceList, int, droppedResource) {
	items := make([]v1.StackResource, 0, len(in.Items))
	var (
		dropped int
		first   droppedResource
	)
	for i, it := range in.Items {
		if field, reason := invalidResource(it); field != "" {
			if dropped == 0 {
				first = droppedResource{index: i, field: field, reason: reason}
			}
			dropped++
			continue
		}
		items = append(items, v1.StackResource{
			Address:  it.Address,
			Type:     it.Type,
			Module:   it.Module,
			Provider: it.Provider,
			Tainted:  it.Tainted,
			Deposed:  it.Deposed,
		})
	}
	slices.SortStableFunc(items, func(a, b v1.StackResource) int { return cmp.Compare(a.Address, b.Address) })
	unique := items[:0]
	for _, it := range items {
		if n := len(unique); n > 0 && unique[n-1].Address == it.Address {
			unique[n-1].Tainted = unique[n-1].Tainted || it.Tainted
			unique[n-1].Deposed = unique[n-1].Deposed || it.Deposed
			continue
		}
		unique = append(unique, it)
	}
	out := v1.ResourceList{Items: unique}
	out.Total = min(max(in.Total, len(unique)), maxResourceTotal)
	out.Truncated = in.Truncated || dropped > 0 || out.Total > len(unique)
	return out, dropped, first
}

func invalidResource(it v1.StackResource) (field, reason string) {
	checks := []struct {
		field    string
		value    string
		required bool
		max      int
	}{
		{"address", it.Address, true, maxResourceAddressBytes},
		{"type", it.Type, true, maxResourceTypeBytes},
		{"module", it.Module, false, maxResourceAddressBytes},
		{"provider", it.Provider, false, maxResourceTypeBytes},
	}
	for _, c := range checks {
		if reason := invalidResourceField(c.value, c.required, c.max); reason != "" {
			return c.field, reason
		}
	}
	return "", ""
}

func invalidResourceField(v string, required bool, maxBytes int) string {
	switch {
	case v == "" && required:
		return "is empty"
	case len(v) > maxBytes:
		return fmt.Sprintf("is over %d bytes", maxBytes)
	case !utf8.ValidString(v):
		return "is not valid UTF-8"
	case strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }):
		return "holds a control or format character"
	}
	return ""
}
