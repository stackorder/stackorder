package ui

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	apiDir      = "../../api/v1"
	typesTS     = "../../ui/src/api/types.ts"
	fixturesDir = "../../ui/src/fixtures"
)

type goField struct {
	omitempty bool
	nullable  bool
}

type goAPI struct {
	structs map[string]map[string]goField
	enums   map[string][]string
}

func parseGoAPI(t *testing.T) goAPI {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(apiDir, "*.go"))
	if err != nil {
		t.Fatalf("glob api: %v", err)
	}
	api := goAPI{structs: map[string]map[string]goField{}, enums: map[string][]string{}}
	fset := token.NewFileSet()
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						api.structs[s.Name.Name] = structFields(st)
					}
				case *ast.ValueSpec:
					ident, ok := s.Type.(*ast.Ident)
					if !ok || gen.Tok != token.CONST || len(s.Values) != 1 {
						continue
					}
					lit, ok := s.Values[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unquote %s: %v", lit.Value, err)
					}
					api.enums[ident.Name] = append(api.enums[ident.Name], value)
				}
			}
		}
	}
	return api
}

func structFields(st *ast.StructType) map[string]goField {
	fields := map[string]goField{}
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			continue
		}
		name, opts, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		var nullable bool
		switch f.Type.(type) {
		case *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.InterfaceType:
			nullable = true
		case *ast.Ident:
			nullable = f.Type.(*ast.Ident).Name == "any"
		}
		fields[name] = goField{omitempty: strings.Contains(opts, "omitempty"), nullable: nullable}
	}
	return fields
}

type tsField struct {
	optional bool
	typ      string
}

var (
	tsInterface = regexp.MustCompile(`(?s)export interface (\w+)(?:<[^>]*>)? \{\n(.*?)\n\}`)
	tsFieldLine = regexp.MustCompile(`^\s+(\w+)(\?)?: (.+);$`)
	tsUnion     = regexp.MustCompile(`(?s)export type (\w+) =\s*((?:\s*\|?\s*'[^']*')+);`)
	tsLiteral   = regexp.MustCompile(`'([^']*)'`)
)

func parseTS(t *testing.T) (map[string]map[string]tsField, map[string][]string) {
	t.Helper()
	src, err := os.ReadFile(typesTS)
	if err != nil {
		t.Fatalf("read %s: %v", typesTS, err)
	}
	interfaces := map[string]map[string]tsField{}
	for _, m := range tsInterface.FindAllStringSubmatch(string(src), -1) {
		fields := map[string]tsField{}
		for _, line := range strings.Split(m[2], "\n") {
			f := tsFieldLine.FindStringSubmatch(line)
			if f == nil {
				t.Fatalf("types.ts: interface %s: cannot parse line %q", m[1], line)
			}
			fields[f[1]] = tsField{optional: f[2] == "?", typ: f[3]}
		}
		interfaces[m[1]] = fields
	}
	unions := map[string][]string{}
	for _, m := range tsUnion.FindAllStringSubmatch(string(src), -1) {
		for _, lit := range tsLiteral.FindAllStringSubmatch(m[2], -1) {
			unions[m[1]] = append(unions[m[1]], lit[1])
		}
	}
	return interfaces, unions
}

func goName(tsName string) string {
	if tsName == "ApiErrorBody" {
		return "Error"
	}
	return tsName
}

func TestTypesMirrorAPI(t *testing.T) {
	api := parseGoAPI(t)
	interfaces, unions := parseTS(t)

	required := []string{
		"AffectedStack", "ApiErrorBody", "Backend", "Check", "CreateRunResponse", "DriftStatus",
		"Edge", "Graph", "GraphView", "LockInfo", "Matrix", "MatrixEntry", "Module", "ModuleConsume",
		"ModuleConsumer", "ModuleDetail", "ModuleVersion", "NodeRef", "Overview", "Page", "PlanSummary",
		"RepoSummary", "Run", "RunStack", "RunStackRef", "Stack", "StackApplyConfig", "StackConfig",
		"StackDetail", "UnlockRequest", "UnlockResponse", "Whoami",
	}
	for _, name := range required {
		if _, ok := interfaces[name]; !ok {
			t.Errorf("types.ts does not mirror %s", name)
		}
	}

	for tsName, fields := range interfaces {
		t.Run(tsName, func(t *testing.T) {
			goFields, ok := api.structs[goName(tsName)]
			if !ok {
				t.Fatalf("types.ts interface %s has no Go struct %s in api/v1", tsName, goName(tsName))
			}
			for name, gf := range goFields {
				tf, ok := fields[name]
				if !ok {
					t.Errorf("field %q is missing from types.ts", name)
					continue
				}
				if tf.optional != gf.omitempty {
					t.Errorf("field %q: optional in types.ts = %v, omitempty in Go = %v", name, tf.optional, gf.omitempty)
				}
				if !gf.omitempty && gf.nullable && !strings.HasSuffix(tf.typ, "| null") {
					t.Errorf("field %q: Go encodes a nil value as null, so types.ts must allow null, got %q", name, tf.typ)
				}
			}
			for name := range fields {
				if _, ok := goFields[name]; !ok {
					t.Errorf("types.ts field %q does not exist in Go", name)
				}
			}
		})
	}

	for tsName, values := range unions {
		t.Run("enum "+tsName, func(t *testing.T) {
			goValues, ok := api.enums[tsName]
			if !ok {
				t.Fatalf("types.ts union %s has no Go string constants", tsName)
			}
			got, want := append([]string(nil), values...), append([]string(nil), goValues...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("union %s = %v, Go constants = %v", tsName, got, want)
			}
		})
	}
}

var enumValues = map[reflect.Type][]string{
	reflect.TypeFor[v1.NodeKind]():     {"stack", "module"},
	reflect.TypeFor[v1.ModuleKind]():   {"local", "git", "registry"},
	reflect.TypeFor[v1.EdgeType]():     {"depends_on", "uses_module", "reads_state"},
	reflect.TypeFor[v1.Tool]():         {"terraform", "tofu"},
	reflect.TypeFor[v1.RunMode]():      {"plan", "apply", "drift"},
	reflect.TypeFor[v1.Trigger]():      {"pull_request", "comment", "push", "schedule", "rerequest", "manual"},
	reflect.TypeFor[v1.RunStatus]():    {"pending", "planning", "planned", "applying", "applied", "failed", "unconfirmed", "superseded"},
	reflect.TypeFor[v1.StackStatus]():  {"pending", "planning", "planned", "applying", "applied", "failed", "blocked", "noop", "unconfirmed", "unknown", "skipped"},
	reflect.TypeFor[v1.CheckStatus]():  {"pass", "fail", "warn"},
	reflect.TypeFor[v1.Reason]():       {"changed", "module", "dependent", "requested", "reads_state"},
	reflect.TypeFor[v1.ResultStatus](): {"success", "failure", "error"},
}

func checkEnums(t *testing.T, where string, v reflect.Value) {
	t.Helper()
	if allowed, ok := enumValues[v.Type()]; ok {
		s := v.String()
		for _, a := range allowed {
			if s == a {
				return
			}
		}
		t.Errorf("%s: %q is not a valid %s", where, s, v.Type().Name())
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			checkEnums(t, where, v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() || (strings.Contains(field.Tag.Get("json"), "omitempty") && v.Field(i).IsZero()) {
				continue
			}
			checkEnums(t, where+"."+field.Name, v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			checkEnums(t, where+"["+strconv.Itoa(i)+"]", v.Index(i))
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			checkEnums(t, where+"{key}", iter.Key())
			checkEnums(t, where+"["+iter.Key().String()+"]", iter.Value())
		}
	default:
	}
}

func TestFixturesAreAPIResponses(t *testing.T) {
	decoders := map[string]func() any{
		"me.json":          func() any { return new(v1.Whoami) },
		"overview.json":    func() any { return new(v1.Overview) },
		"repos.json":       func() any { return new(v1.Page[v1.RepoSummary]) },
		"graph.json":       func() any { return new(v1.GraphView) },
		"graph-run.json":   func() any { return new(v1.GraphView) },
		"runs.json":        func() any { return new(v1.Page[v1.Run]) },
		"run.json":         func() any { return new(v1.Run) },
		"stack.json":       func() any { return new(v1.StackDetail) },
		"stack-runs.json":  func() any { return new(v1.Page[v1.RunStackRef]) },
		"repo-stacks.json": func() any { return new(v1.Page[v1.StackDetail]) },
		"modules.json":     func() any { return new(v1.Page[v1.ModuleDetail]) },
		"module.json":      func() any { return new(v1.ModuleDetail) },
	}
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixtures in %s", fixturesDir)
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			newValue, ok := decoders[name]
			if !ok {
				t.Fatalf("fixture %s has no API type; add it to the decoders table", name)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			value := newValue()
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(value); err != nil {
				t.Fatalf("decode as %T: %v", value, err)
			}
			checkEnums(t, name, reflect.ValueOf(value))

			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var want, got any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("unmarshal round trip: %v", err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("fixture is not what the server would encode for %T:\nfixture: %s\nencoded: %s", value, compact(t, data), encoded)
			}
		})
	}
}

func compact(t *testing.T, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.String()
}
