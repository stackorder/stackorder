package tf

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	tfjson "github.com/hashicorp/terraform-json"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// ShowStateJSONRaw runs `show -json -no-color` with no file and returns
// stdout, the current workspace's persisted state. The JSON holds every
// attribute value, sensitive ones included, in clear text; it is never
// streamed.
func (r *Runner) ShowStateJSONRaw(ctx context.Context) ([]byte, error) {
	return r.capture(ctx, []string{"show", "-json", "-no-color"})
}

// ParseStateJSON decodes `show -json` state output and validates its format
// version. Its errors never quote the input.
func ParseStateJSON(data []byte) (*tfjson.State, error) {
	var s tfjson.State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, stateJSONError(err)
	}
	return &s, nil
}

func stateJSONError(err error) error {
	var (
		syntax  *json.SyntaxError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &syntax):
		return fmt.Errorf("tf: decoding state JSON: not valid JSON at byte %d", syntax.Offset)
	case errors.As(err, &typeErr):
		return fmt.Errorf("tf: decoding state JSON: unexpected value at byte %d", typeErr.Offset)
	}
	return errors.New("tf: decoding state JSON: missing or unsupported format version")
}

// Resources lists the managed resources of a state module and its child
// modules, sorted by address, with no attribute, identity or dependency
// value. Module is the module's address, Provider its provider name. All
// objects at one address fold into one entry whose flags are ORed; a
// deposed object sets Deposed. Data sources are left out. A nil module is
// an empty list.
func Resources(root *tfjson.StateModule) []v1.StackResource {
	out := []v1.StackResource{}
	index := map[string]int{}
	var walk func(m *tfjson.StateModule)
	walk = func(m *tfjson.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			if r == nil || r.Mode != tfjson.ManagedResourceMode {
				continue
			}
			item := v1.StackResource{
				Address:  r.Address,
				Type:     r.Type,
				Module:   m.Address,
				Provider: r.ProviderName,
				Tainted:  r.Tainted,
				Deposed:  r.DeposedKey != "",
			}
			if i, ok := index[item.Address]; ok {
				out[i].Tainted = out[i].Tainted || item.Tainted
				out[i].Deposed = out[i].Deposed || item.Deposed
				continue
			}
			index[item.Address] = len(out)
			out = append(out, item)
		}
		for _, child := range m.ChildModules {
			walk(child)
		}
	}
	walk(root)
	slices.SortStableFunc(out, compareAddress)
	return out
}

// RedactResources masks secrets in the Address, Module, Type and Provider
// of every item with r, then folds items whose redacted addresses collide
// into the first of them, ORing their flags, and sorts by address.
func RedactResources(items []v1.StackResource, r *Redactor) []v1.StackResource {
	red := make([]v1.StackResource, 0, len(items))
	for _, it := range items {
		red = append(red, v1.StackResource{
			Address:  r.Redact(it.Address),
			Type:     r.Redact(it.Type),
			Module:   r.Redact(it.Module),
			Provider: r.Redact(it.Provider),
			Tainted:  it.Tainted,
			Deposed:  it.Deposed,
		})
	}
	slices.SortStableFunc(red, compareAddress)
	out := red[:0]
	for _, it := range red {
		if n := len(out); n > 0 && out[n-1].Address == it.Address {
			out[n-1].Tainted = out[n-1].Tainted || it.Tainted
			out[n-1].Deposed = out[n-1].Deposed || it.Deposed
			continue
		}
		out = append(out, it)
	}
	return out
}

// FitResources returns a copy of list holding the longest prefix of its
// items whose encoding/json form, the list's envelope included, fits in
// budget bytes. Total is kept, and Truncated is set when any item was left
// out. A budget of zero or less keeps no item. A nil list stays nil.
func FitResources(list *v1.ResourceList, budget int) *v1.ResourceList {
	if list == nil {
		return nil
	}
	out := &v1.ResourceList{Total: list.Total, Truncated: list.Truncated, Items: []v1.StackResource{}}
	sizes := make([]int, 0, len(list.Items))
	itemsSize := 0
	for i, it := range list.Items {
		data, err := json.Marshal(it)
		if err != nil {
			break
		}
		sizes = append(sizes, len(data))
		itemsSize += len(data)
		if i > 0 {
			itemsSize++
		}
	}
	if len(sizes) == len(list.Items) && envelopeSize(out.Total, out.Truncated)+itemsSize <= budget {
		out.Items = append(out.Items, list.Items...)
		return out
	}
	size := envelopeSize(out.Total, true)
	for i, n := range sizes {
		size += n
		if i > 0 {
			size++
		}
		if size > budget {
			break
		}
		out.Items = append(out.Items, list.Items[i])
	}
	out.Truncated = out.Truncated || len(out.Items) < len(list.Items)
	return out
}

func envelopeSize(total int, truncated bool) int {
	data, _ := json.Marshal(v1.ResourceList{Total: total, Truncated: truncated, Items: []v1.StackResource{}})
	return len(data)
}

func compareAddress(a, b v1.StackResource) int {
	return cmp.Compare(a.Address, b.Address)
}
