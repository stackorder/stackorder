package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	backendS3          = "s3"
	remoteStateType    = "terraform_remote_state"
	defaultWorkspace   = "default"
	defaultWorkspaceKP = "env:"
)

var (
	fileSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "terraform"},
		{Type: "data", LabelNames: []string{"type", "name"}},
	}}
	terraformSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "backend", LabelNames: []string{"type"}},
	}}
	s3Attributes = []string{"bucket", "key", "region", "dynamodb_table", "use_lockfile", "workspace_key_prefix"}
	s3Schema     = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "bucket"}, {Name: "key"}, {Name: "region"},
		{Name: "dynamodb_table"}, {Name: "use_lockfile"}, {Name: "workspace_key_prefix"},
	}}
	remoteStateSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "backend"}, {Name: "config"}, {Name: "workspace"},
	}}
)

type dirConfig struct {
	backendType  string
	backendLoc   string
	backend      *v1.Backend
	remoteStates []remoteState
}

type remoteState struct {
	name      string
	loc       string
	backend   string
	bucket    string
	key       string
	prefix    string
	workspace string
	problem   string
}

func (r remoteState) objectKey() string {
	return stateObjectKey(r.prefix, r.workspace, r.key)
}

func stateObjectKey(prefix, workspace, key string) string {
	if workspace == "" || workspace == defaultWorkspace {
		return key
	}
	if prefix == "" {
		prefix = defaultWorkspaceKP
	}
	return prefix + "/" + workspace + "/" + key
}

type hclReader struct {
	fsys   *os.Root
	parser *hclparse.Parser
	warn   func(format string, args ...any)
}

func (r *hclReader) readDir(dir string, names []string) dirConfig {
	var out dirConfig
	primaries, overrides := splitOverrides(names)
	for i, name := range append(primaries, overrides...) {
		rel := path.Join(dir, name)
		body, ok := r.parseFile(rel)
		if !ok {
			continue
		}
		content, _, diags := body.PartialContent(fileSchema)
		r.diagnostics(diags)
		for _, block := range content.Blocks {
			switch block.Type {
			case "terraform":
				r.readTerraformBlock(block, i >= len(primaries), &out)
			case "data":
				if block.Labels[0] == remoteStateType {
					out.remoteStates = append(out.remoteStates, r.readRemoteState(block))
				}
			}
		}
	}
	return out
}

func splitOverrides(names []string) (primaries, overrides []string) {
	for _, name := range names {
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".tf")
		if base == "override" || strings.HasSuffix(base, "_override") {
			overrides = append(overrides, name)
		} else {
			primaries = append(primaries, name)
		}
	}
	return primaries, overrides
}

func (r *hclReader) parseFile(rel string) (hcl.Body, bool) {
	src, err := r.fsys.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		r.warn("%s: %v", rel, pathError(err))
		return nil, false
	}
	var (
		file  *hcl.File
		diags hcl.Diagnostics
	)
	if strings.HasSuffix(rel, ".json") {
		file, diags = r.parser.ParseJSON(src, rel)
	} else {
		file, diags = r.parser.ParseHCL(src, rel)
	}
	r.diagnostics(diags)
	if file == nil || file.Body == nil {
		return nil, false
	}
	return file.Body, true
}

func (r *hclReader) diagnostics(diags hcl.Diagnostics) {
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		if d.Subject != nil {
			r.warn("%s: %s", location(*d.Subject), d.Summary)
		} else {
			r.warn("%s", d.Summary)
		}
	}
}

func (r *hclReader) readTerraformBlock(block *hcl.Block, override bool, out *dirConfig) {
	content, _, diags := block.Body.PartialContent(terraformSchema)
	r.diagnostics(diags)
	for _, b := range content.Blocks {
		loc := location(b.DefRange)
		if out.backendType != "" && !override {
			r.warn("%s: duplicate backend block; using the one at %s", loc, out.backendLoc)
			continue
		}
		out.backendType, out.backendLoc, out.backend = b.Labels[0], loc, nil
		if b.Labels[0] == backendS3 {
			out.backend = r.readS3Backend(b)
		}
	}
}

func (r *hclReader) readS3Backend(block *hcl.Block) *v1.Backend {
	content, _, diags := block.Body.PartialContent(s3Schema)
	r.diagnostics(diags)
	be := &v1.Backend{Type: backendS3}
	for _, name := range s3Attributes {
		attr, ok := content.Attributes[name]
		if !ok {
			continue
		}
		if name == "use_lockfile" {
			v, ok := literalBool(attr.Expr)
			if !ok {
				r.warn("%s: backend %q attribute %q is not a literal value; left empty", location(attr.Range), backendS3, name)
			}
			be.UseLockfile = v
			continue
		}
		v, ok := literalString(attr.Expr)
		if !ok {
			r.warn("%s: backend %q attribute %q is not a literal value; left empty", location(attr.Range), backendS3, name)
			continue
		}
		switch name {
		case "bucket":
			be.Bucket = v
		case "key":
			be.Key = v
		case "region":
			be.Region = v
		case "dynamodb_table":
			be.DynamoDBTable = v
		case "workspace_key_prefix":
			be.WorkspaceKeyPrefix = v
		}
	}
	return be
}

func (r *hclReader) readRemoteState(block *hcl.Block) remoteState {
	rs := remoteState{name: block.Labels[1], loc: location(block.DefRange)}
	content, _, diags := block.Body.PartialContent(remoteStateSchema)
	r.diagnostics(diags)
	fail := func(rng hcl.Range, format string, args ...any) remoteState {
		rs.loc, rs.problem = location(rng), fmt.Sprintf(format, args...)
		return rs
	}

	attr, ok := content.Attributes["backend"]
	if !ok {
		return fail(block.DefRange, "backend is not set")
	}
	if rs.backend, ok = literalString(attr.Expr); !ok {
		return fail(attr.Range, "backend is not a literal value")
	}
	if rs.backend != backendS3 {
		return rs
	}
	if attr, ok := content.Attributes["workspace"]; ok {
		if rs.workspace, ok = literalString(attr.Expr); !ok {
			return fail(attr.Range, "workspace is not a literal value")
		}
	}
	cfg, ok := content.Attributes["config"]
	if !ok {
		return fail(block.DefRange, "config is not set")
	}
	pairs, diags := hcl.ExprMap(cfg.Expr)
	if diags.HasErrors() {
		return fail(cfg.Range, "config is not an object literal")
	}
	for _, pair := range pairs {
		name, ok := keyName(pair.Key)
		if !ok {
			return fail(pair.Key.Range(), "config has a key that is not a literal value")
		}
		var dst *string
		switch name {
		case "bucket":
			dst = &rs.bucket
		case "key":
			dst = &rs.key
		case "workspace_key_prefix":
			dst = &rs.prefix
		default:
			continue
		}
		if *dst, ok = literalString(pair.Value); !ok {
			return fail(pair.Value.Range(), "config.%s is not a literal value", name)
		}
	}
	if rs.bucket == "" {
		return fail(cfg.Range, "config.bucket is not set")
	}
	if rs.key == "" {
		return fail(cfg.Range, "config.key is not set")
	}
	return rs
}

func keyName(expr hcl.Expression) (string, bool) {
	if k, ok := expr.(*hclsyntax.ObjectConsKeyExpr); ok {
		if kw := hcl.ExprAsKeyword(k); kw != "" && !k.ForceNonLiteral {
			return kw, true
		}
		expr = k.Wrapped
	}
	return literalString(expr)
}

func isLiteral(expr hcl.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		return true
	case *hclsyntax.TemplateExpr:
		return e.IsStringLiteral()
	case hclsyntax.Expression:
		return false
	}
	return len(expr.Variables()) == 0
}

func literalString(expr hcl.Expression) (string, bool) {
	var s string
	if !isLiteral(expr) || gohcl.DecodeExpression(expr, nil, &s).HasErrors() {
		return "", false
	}
	return s, true
}

func literalBool(expr hcl.Expression) (bool, bool) {
	var b bool
	if !isLiteral(expr) || gohcl.DecodeExpression(expr, nil, &b).HasErrors() {
		return false, false
	}
	return b, true
}

func pathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func location(rng hcl.Range) string {
	return fmt.Sprintf("%s:%d", rng.Filename, rng.Start.Line)
}
