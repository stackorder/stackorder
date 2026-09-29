package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"text/template"
	"text/template/parse"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// MaxRenderedLength is the longest text a template, or any function it
// calls, may produce.
const MaxRenderedLength = 4096

// TemplateData is what a configuration template renders with: .Path, .Name,
// .Instance and .Key.
type TemplateData struct {
	// Path is the normalized repository relative stack directory.
	Path string
	// Name is the last segment of Path.
	Name string
	// Instance is the instance name, empty for an unnamed instance.
	Instance string
	// Key is v1.StackKey(Path, Instance).
	Key string
}

// TemplateDataFor builds the template data of one instance of a stack
// directory.
func TemplateDataFor(stackPath, instance string) TemplateData {
	stackPath = NormalizePath(stackPath)
	name := ""
	if stackPath != "" {
		name = path.Base(stackPath)
	}
	return TemplateData{Path: stackPath, Name: name, Instance: instance, Key: v1.StackKey(stackPath, instance)}
}

var errTooLong = fmt.Errorf("output is longer than %d bytes", MaxRenderedLength)

func capped(s string) (string, error) {
	if len(s) > MaxRenderedLength {
		return "", errTooLong
	}
	return s, nil
}

var templateFuncs = template.FuncMap{
	"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	"base":       path.Base,
	"dir":        path.Dir,
	"replace": func(from, to, s string) (string, error) {
		if n := strings.Count(s, from); len(s)+n*(len(to)-len(from)) > MaxRenderedLength {
			return "", errTooLong
		}
		return strings.ReplaceAll(s, from, to), nil
	},
	"lower": func(s string) (string, error) { return capped(strings.ToLower(s)) },
	"upper": func(s string) (string, error) { return capped(strings.ToUpper(s)) },
}

var allowedBuiltins = map[string]bool{
	"and": true, "or": true, "not": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
}

var sampleTemplateData = TemplateDataFor("stacks/sample", "sample")

// Render expands a configuration template with Go text/template, failing on
// a missing key. A string without "{{" is returned as is. Templates may use
// fields, pipelines, variables, if and with, the functions trimPrefix,
// trimSuffix, base, dir, replace, lower and upper, and the comparison and
// logic builtins; range, define, template and block are refused, and the
// output may not exceed MaxRenderedLength bytes.
func Render(text string, data TemplateData) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New("").Option("missingkey=error").Funcs(templateFuncs).Parse(text)
	if err == nil {
		err = checkTree(t)
	}
	if err != nil {
		return "", fmt.Errorf("template %q: %w", text, err)
	}
	w := &limitedWriter{}
	values := map[string]string{"Path": data.Path, "Name": data.Name, "Instance": data.Instance, "Key": data.Key}
	if err := t.Execute(w, values); err != nil {
		if errors.Is(err, errTooLong) {
			err = errTooLong
		}
		return "", fmt.Errorf("template %q: %w", text, err)
	}
	return w.b.String(), nil
}

type limitedWriter struct{ b strings.Builder }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.b.Len()+len(p) > MaxRenderedLength {
		return 0, errTooLong
	}
	return w.b.Write(p)
}

func checkTree(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errors.New("define and block are not supported")
	}
	if t.Tree == nil {
		return nil
	}
	return checkNode(t.Root)
}

func checkNode(n parse.Node) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := checkNode(c); err != nil {
				return err
			}
		}
		return nil
	case *parse.ActionNode:
		return checkNode(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Cmds {
			if err := checkNode(c); err != nil {
				return err
			}
		}
		return nil
	case *parse.CommandNode:
		for _, a := range n.Args {
			if err := checkNode(a); err != nil {
				return err
			}
		}
		return nil
	case *parse.ChainNode:
		return checkNode(n.Node)
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.IdentifierNode:
		if _, ok := templateFuncs[n.Ident]; ok || allowedBuiltins[n.Ident] {
			return nil
		}
		return fmt.Errorf("function %q is not supported", n.Ident)
	case *parse.TextNode, *parse.FieldNode, *parse.VariableNode, *parse.DotNode,
		*parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode, *parse.CommentNode:
		return nil
	case *parse.RangeNode:
		return errors.New("range is not supported")
	case *parse.TemplateNode:
		return errors.New("template is not supported")
	case *parse.BreakNode, *parse.ContinueNode:
		return errors.New("break and continue are not supported")
	default:
		return fmt.Errorf("%T is not supported", n)
	}
}

func checkBranch(b *parse.BranchNode) error {
	for _, n := range []parse.Node{b.Pipe, b.List, b.ElseList} {
		if err := checkNode(n); err != nil {
			return err
		}
	}
	return nil
}

func checkTemplate(text string) error {
	_, err := Render(text, sampleTemplateData)
	return err
}

func isTemplate(s string) bool { return strings.Contains(s, "{{") }
