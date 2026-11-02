package runs

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestCleanResources(t *testing.T) {
	vpc := v1.StackResource{Address: "aws_vpc.main", Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws"}
	subnet := v1.StackResource{Address: `module.net.aws_subnet.a["eu-west-1a/x.y"]`, Type: "aws_subnet", Module: "module.net"}
	with := func(r v1.StackResource, edit func(*v1.StackResource)) v1.StackResource {
		edit(&r)
		return r
	}
	tests := []struct {
		name    string
		in      v1.ResourceList
		want    v1.ResourceList
		dropped int
		first   droppedResource
	}{
		{
			name: "valid list is sorted and kept",
			in:   v1.ResourceList{Total: 2, Items: []v1.StackResource{vpc, subnet}},
			want: v1.ResourceList{Total: 2, Items: []v1.StackResource{vpc, subnet}},
		},
		{
			name: "empty list",
			in:   v1.ResourceList{},
			want: v1.ResourceList{Items: []v1.StackResource{}},
		},
		{
			name: "last change is never taken from a job",
			in: v1.ResourceList{Total: 1, Items: []v1.StackResource{
				with(vpc, func(r *v1.StackResource) {
					r.LastChange = &v1.ResourceChange{Action: v1.ResourceCreate, RunID: "forged"}
				}),
			}},
			want: v1.ResourceList{Total: 1, Items: []v1.StackResource{vpc}},
		},
		{
			name: "duplicates fold into one entry with their flags ored",
			in: v1.ResourceList{Total: 2, Items: []v1.StackResource{
				with(vpc, func(r *v1.StackResource) { r.Tainted = true }),
				subnet,
				with(vpc, func(r *v1.StackResource) { r.Type, r.Deposed = "aws_other", true }),
			}},
			want: v1.ResourceList{Total: 2, Items: []v1.StackResource{
				with(vpc, func(r *v1.StackResource) { r.Tainted, r.Deposed = true, true }),
				subnet,
			}},
		},
		{
			name: "total is at least the number of items",
			in:   v1.ResourceList{Total: -4, Items: []v1.StackResource{vpc, subnet}},
			want: v1.ResourceList{Total: 2, Items: []v1.StackResource{vpc, subnet}},
		},
		{
			name: "total over the bound is clamped and marks the list truncated",
			in:   v1.ResourceList{Total: 1 << 40, Items: []v1.StackResource{vpc}},
			want: v1.ResourceList{Total: maxResourceTotal, Truncated: true, Items: []v1.StackResource{vpc}},
		},
		{
			name: "a truncated list stays truncated",
			in:   v1.ResourceList{Total: 1, Truncated: true, Items: []v1.StackResource{vpc}},
			want: v1.ResourceList{Total: 1, Truncated: true, Items: []v1.StackResource{vpc}},
		},
		{
			name: "invalid items are dropped and the rest kept",
			in: v1.ResourceList{Total: 9, Items: []v1.StackResource{
				vpc,
				with(vpc, func(r *v1.StackResource) { r.Address = "" }),
				with(vpc, func(r *v1.StackResource) { r.Type = "" }),
				with(vpc, func(r *v1.StackResource) { r.Address = "aws_vpc.\u202emain" }),
				with(vpc, func(r *v1.StackResource) { r.Address = "aws_vpc.\u200bmain" }),
				with(vpc, func(r *v1.StackResource) { r.Module = "module.a\nmodule.b" }),
				with(vpc, func(r *v1.StackResource) { r.Provider = "registry\xff" }),
				with(vpc, func(r *v1.StackResource) { r.Address = strings.Repeat("a", maxResourceAddressBytes+1) }),
				with(vpc, func(r *v1.StackResource) { r.Type = strings.Repeat("t", maxResourceTypeBytes+1) }),
				subnet,
			}},
			want:    v1.ResourceList{Total: 9, Truncated: true, Items: []v1.StackResource{vpc, subnet}},
			dropped: 8,
			first:   droppedResource{index: 1, field: "address", reason: "is empty"},
		},
		{
			name: "limits are inclusive",
			in: v1.ResourceList{Total: 1, Items: []v1.StackResource{{
				Address: strings.Repeat("a", maxResourceAddressBytes), Type: strings.Repeat("t", maxResourceTypeBytes),
				Module: strings.Repeat("m", maxResourceAddressBytes), Provider: strings.Repeat("p", maxResourceTypeBytes),
			}}},
			want: v1.ResourceList{Total: 1, Items: []v1.StackResource{{
				Address: strings.Repeat("a", maxResourceAddressBytes), Type: strings.Repeat("t", maxResourceTypeBytes),
				Module: strings.Repeat("m", maxResourceAddressBytes), Provider: strings.Repeat("p", maxResourceTypeBytes),
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped, first := cleanResources(tt.in)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("cleanResources (-want +got):\n%s", diff)
			}
			assert.Equal(t, tt.dropped, dropped)
			assert.Equal(t, tt.first, first)
		})
	}
}

func TestInvalidResource(t *testing.T) {
	ok := v1.StackResource{Address: "aws_vpc.main", Type: "aws_vpc"}
	tests := []struct {
		name   string
		edit   func(*v1.StackResource)
		field  string
		reason string
	}{
		{name: "valid", edit: func(*v1.StackResource) {}},
		{name: "no module or provider is fine", edit: func(r *v1.StackResource) { r.Module, r.Provider = "", "" }},
		{name: "empty address", edit: func(r *v1.StackResource) { r.Address = "" }, field: "address", reason: "is empty"},
		{name: "empty type", edit: func(r *v1.StackResource) { r.Type = "" }, field: "type", reason: "is empty"},
		{name: "long address", edit: func(r *v1.StackResource) { r.Address = strings.Repeat("a", 2049) }, field: "address", reason: "is over 2048 bytes"},
		{name: "long module", edit: func(r *v1.StackResource) { r.Module = strings.Repeat("m", 2049) }, field: "module", reason: "is over 2048 bytes"},
		{name: "long type", edit: func(r *v1.StackResource) { r.Type = strings.Repeat("t", 513) }, field: "type", reason: "is over 512 bytes"},
		{name: "long provider", edit: func(r *v1.StackResource) { r.Provider = strings.Repeat("p", 513) }, field: "provider", reason: "is over 512 bytes"},
		{name: "invalid UTF-8", edit: func(r *v1.StackResource) { r.Type = "aws_\xc3" }, field: "type", reason: "is not valid UTF-8"},
		{name: "NUL", edit: func(r *v1.StackResource) { r.Address = "aws_vpc.\x00" }, field: "address", reason: "holds a control or format character"},
		{name: "DEL", edit: func(r *v1.StackResource) { r.Provider = "aws\x7f" }, field: "provider", reason: "holds a control or format character"},
		{name: "C1 control", edit: func(r *v1.StackResource) { r.Module = "module.\u0085a" }, field: "module", reason: "holds a control or format character"},
		{name: "right-to-left override", edit: func(r *v1.StackResource) { r.Address = "aws_vpc.\u202e" }, field: "address", reason: "holds a control or format character"},
		{name: "zero-width space", edit: func(r *v1.StackResource) { r.Type = "aws\u200bvpc" }, field: "type", reason: "holds a control or format character"},
		{name: "byte order mark", edit: func(r *v1.StackResource) { r.Address = "\ufeffaws_vpc.main" }, field: "address", reason: "holds a control or format character"},
		{name: "for_each key with dots, slashes and unicode", edit: func(r *v1.StackResource) { r.Address = `aws_s3_bucket.b["acme.example/é"]` }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ok
			tt.edit(&r)
			field, reason := invalidResource(r)
			assert.Equal(t, tt.field, field)
			assert.Equal(t, tt.reason, reason)
		})
	}
}
