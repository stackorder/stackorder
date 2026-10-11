package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

const (
	backendS3       = "s3"
	remoteStateType = "terraform_remote_state"
	outputsType     = "stackorder_outputs"
)

var (
	fileSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "terraform"},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "ephemeral", LabelNames: []string{"type", "name"}},
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
	outputsSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "stack"}}}
	moduleSchema  = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "module", LabelNames: []string{"name"}}}}
)

type dirConfig struct {
	backendType  string
	backendLoc   string
	backend      *v1.Backend
	remoteStates []remoteState
	outputReads  []outputRead
}

type outputRead struct {
	name       string
	kind       string
	loc        string
	stack      string
	literal    bool
	expr       hcl.Expression
	module     string
	inStackDir bool
}

func (o outputRead) address() string {
	a := o.kind + "." + outputsType + "." + o.name
	if o.module != "" {
		a += " in module " + o.module
	}
	return a
}

func (o outputRead) resolve(call moduleArguments) (outputRead, bool) {
	if o.literal || o.expr == nil {
		return o, false
	}
	vars := map[string]cty.Value{}
	for _, t := range o.expr.Variables() {
		name, ok := variableName(t)
		if !ok {
			return o, false
		}
		arg, ok := call.args[name]
		if !ok || !isLiteral(arg.Expr) {
			return o, false
		}
		v, diags := arg.Expr.Value(nil)
		if diags.HasErrors() {
			return o, false
		}
		vars[name] = v
	}
	if len(vars) == 0 {
		return o, false
	}
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"var": cty.ObjectVal(vars)}}
	var stack string
	if gohcl.DecodeExpression(o.expr, ctx, &stack).HasErrors() {
		return o, false
	}
	o.stack, o.literal, o.loc = stack, true, call.loc
	return o, true
}

func variableName(t hcl.Traversal) (string, bool) {
	if len(t) < 2 || t.RootName() != "var" {
		return "", false
	}
	attr, ok := t[1].(hcl.TraverseAttr)
	return attr.Name, ok
}

type moduleArguments struct {
	loc  string
	args hcl.Attributes
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
	return config.StateObjectKey(r.prefix, r.workspace, r.key)
}

type hclReader struct {
	fsys   *os.Root
	parser *hclparse.Parser
	warn   func(format string, args ...any)
}

func (r *hclReader) readDir(dir string, names []string) dirConfig {
	var out dirConfig
	r.eachBlock(dir, names, fileSchema, func(block *hcl.Block, override bool) {
		switch {
		case block.Type == "terraform":
			r.readTerraformBlock(block, override, &out)
		case block.Type == "data" && block.Labels[0] == remoteStateType:
			out.remoteStates = append(out.remoteStates, r.readRemoteState(block))
		case isOutputsBlock(block):
			r.readOutputRead(block, override, &out)
		}
	})
	return out
}

func (r *hclReader) readModuleOutputReads(dir string, names []string) []outputRead {
	var out dirConfig
	r.eachBlock(dir, names, fileSchema, func(block *hcl.Block, override bool) {
		if isOutputsBlock(block) {
			r.readOutputRead(block, override, &out)
		}
	})
	for i := range out.outputReads {
		out.outputReads[i].module = displayDir(dir)
	}
	return out.outputReads
}

func (r *hclReader) readModuleArguments(dir string, names []string) map[string]moduleArguments {
	out := map[string]moduleArguments{}
	r.eachBlock(dir, names, moduleSchema, func(block *hcl.Block, override bool) {
		attrs, _ := block.Body.JustAttributes()
		call, ok := out[block.Labels[0]]
		if !ok || !override {
			call = moduleArguments{loc: location(block.DefRange), args: hcl.Attributes{}}
		}
		maps.Copy(call.args, attrs)
		out[block.Labels[0]] = call
	})
	return out
}

func (r *hclReader) eachBlock(dir string, names []string, schema *hcl.BodySchema, fn func(block *hcl.Block, override bool)) {
	primaries, overrides := splitOverrides(names)
	for i, name := range append(primaries, overrides...) {
		body, ok := r.parseFile(path.Join(dir, name))
		if !ok {
			continue
		}
		content, _, diags := body.PartialContent(schema)
		r.diagnostics(diags)
		for _, block := range content.Blocks {
			fn(block, i >= len(primaries))
		}
	}
}

func isOutputsBlock(block *hcl.Block) bool {
	return (block.Type == "data" || block.Type == "ephemeral") && block.Labels[0] == outputsType
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
	r.overlayS3(content.Attributes, be, fmt.Sprintf("backend %q", backendS3), "left empty")
	return be
}

func (r *hclReader) overlayBackendFile(rel string, src []byte, be *v1.Backend) {
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
		return
	}
	content, _, diags := file.Body.PartialContent(s3Schema)
	r.diagnostics(diags)
	r.overlayS3(content.Attributes, be, "backend config", "skipped")
}

func (r *hclReader) overlayS3(attrs hcl.Attributes, be *v1.Backend, what, outcome string) {
	for _, name := range s3Attributes {
		attr, ok := attrs[name]
		if !ok {
			continue
		}
		if name == "use_lockfile" {
			v, ok := literalBool(attr.Expr)
			if !ok {
				r.warn("%s: %s attribute %q is not a literal value; %s", location(attr.Range), what, name, outcome)
				continue
			}
			be.UseLockfile = v
			continue
		}
		v, ok := literalString(attr.Expr)
		if !ok {
			r.warn("%s: %s attribute %q is not a literal value; %s", location(attr.Range), what, name, outcome)
			continue
		}
		setS3String(be, name, v)
	}
}

func setS3String(be *v1.Backend, name, value string) {
	switch name {
	case "bucket":
		be.Bucket = value
	case "key":
		be.Key = value
	case "region":
		be.Region = value
	case "dynamodb_table":
		be.DynamoDBTable = value
	case "workspace_key_prefix":
		be.WorkspaceKeyPrefix = value
	}
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

func (r *hclReader) readOutputRead(block *hcl.Block, override bool, out *dirConfig) {
	content, _, diags := block.Body.PartialContent(outputsSchema)
	r.diagnostics(diags)
	attr, ok := content.Attributes["stack"]
	if !ok {
		return
	}
	read := outputRead{name: block.Labels[1], kind: block.Type, loc: location(attr.Range), expr: attr.Expr}
	read.stack, read.literal = literalString(attr.Expr)
	if override {
		for i, o := range out.outputReads {
			if o.kind == read.kind && o.name == read.name {
				out.outputReads[i] = read
				return
			}
		}
	}
	out.outputReads = append(out.outputReads, read)
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
