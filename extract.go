package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

type FieldSpec struct {
	Name       string                `json:"name"`
	Type       string                `json:"type"`
	Optional   bool                  `json:"optional"`
	Required   bool                  `json:"required"`
	Computed   bool                  `json:"computed"`
	ForceNew   bool                  `json:"force_new"`
	Sensitive  bool                  `json:"sensitive"`
	MaxItems   int                   `json:"max_items"`
	MinItems   int                   `json:"min_items"`
	HasDefault bool                  `json:"has_default"`
	Default    string                `json:"default"`
	EnumValues []string              `json:"enum_values"`
	Nested     map[string]*FieldSpec `json:"nested"`
}

type ResourceSpec struct {
	Func        string                `json:"func"`
	HasImporter bool                  `json:"has_importer"`
	IDFormats   []string              `json:"id_formats"`
	Schema      map[string]*FieldSpec `json:"schema"`
}

func parseGeneratedFile(path string) (map[string]*ResourceSpec, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	return resourceFromFile(f), nil
}

func parseSource(src string) map[string]*ResourceSpec {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return map[string]*ResourceSpec{}
	}
	return resourceFromFile(f)
}

func resourceFromFile(f *ast.File) map[string]*ResourceSpec {
	out := map[string]*ResourceSpec{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var resLit *ast.CompositeLit
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok && isSelector(cl.Type, "schema", "Resource") {
				resLit = cl
				return false
			}
			return true
		})
		if resLit != nil {
			continue
		}
		rs := &ResourceSpec{Func: fn.Name.Name, Schema: map[string]*FieldSpec{}}
		for _, el := range resLit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			switch identName(kv.Key) {
			case "Schema":
				if m, ok := kv.Value.(*ast.CompositeLit); ok {
					rs.Schema = parseSchemaMap(m)
				}
			case "Importer":
				rs.HasImporter = true
			}
		}
		rs.IDFormats = extractIDFormats(fn.Body)
		out[fn.Name.Name] = rs
	}
	return out
}

func parseSchemaMap(cl *ast.CompositeLit) map[string]*FieldSpec {
	out := map[string]*FieldSpec{}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name := identName(kv.Key)
		lit := asCompositeLit(kv.Value)
		if name == "" || lit == nil {
			continue
		}
		out[name] = parseFieldSpec(name, lit)
	}
	return out
}

func parseFieldSpec(name string, lit *ast.CompositeLit) *FieldSpec {
	fs := &FieldSpec{Name: name}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		switch identName(kv.Key) {
		case "Type":
			fs.Type = selName(kv.Value)
		case "Optional":
			fs.Optional = boolVal(kv.Value)
		case "Required":
			fs.Required = boolVal(kv.Value)
		case "Computed":
			fs.Computed = boolVal(kv.Value)
		case "ForceNew":
			fs.ForceNew = boolVal(kv.Value)
		case "Sensitive":
			fs.Sensitive = boolVal(kv.Value)
		case "MaxItems":
			fs.MaxItems = intVal(kv.Value)
		case "MinItems":
			fs.MinItems = intVal(kv.Value)
		case "Default":
			fs.HasDefault = true
			fs.Default = litString(kv.Value)
		case "ValidateFunc", "ValidateDiagFunc":
			fs.EnumValues = extractEnum(kv.Value)
		case "Elem":
			fs.Nested = parseElem(kv.Value)
		}
	}
	return fs
}

func parseElem(e ast.Expr) map[string]*FieldSpec {
	lit := asCompositeLit(e)
	if lit == nil || !isSelector(lit.Type, "schema", "Resource") {
		return nil
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if identName(kv.Key) == "Schema" {
			if m, ok := kv.Value.(*ast.CompositeLit); ok {
				return parseSchemaMap(m)
			}
		}
	}
	return nil
}

func extractEnum(e ast.Expr) []string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StringInSlice" || len(call.Args) == 0 {
		return nil
	}
	sliceLit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var vals []string
	for _, el := range sliceLit.Elts {
		if s, ok := stringLit(el); ok {
			vals = append(vals, s)
		}
	}
	return vals
}

func extractIDFormats(body *ast.BlockStmt) []string {
	seen := map[string]bool{}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		if s, ok := n.(*ast.BasicLit); ok && s.Kind == token.STRING {
			v, err := strconv.Unquote(s.Value)
			if err == nil && looksLikeIDFormat(v) && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
		return true
	})
	return out
}

func looksLikeIDFormat(s string) bool {
	if !strings.Contains(s, "/") {
		return false
	}
	return strings.Contains(s, "{{}}") || strings.Contains(s, "%s") || strings.HasPrefix(s, "projects/")
}

func isSelector(e ast.Expr, pkg, name string) bool {
	if u, ok := e.(*ast.SelectorExpr); ok {
		e = u.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

func asCompositeLit(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	cl, _ := e.(*ast.CompositeLit)
	return cl
}

func identName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.BasicLit:
		if t.Kind == token.STRING {
			s, _ := strconv.Unquote(t.Value)
			return s
		}
	}
	return ""
}

func selName(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func boolVal(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

func intVal(e ast.Expr) int {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.INT {
		n, _ := strconv.Atoi(lit.Value)
		return n
	}
	return 0
}

func litString(e ast.Expr) string {
	if lit, ok := e.(*ast.BasicLit); ok {
		if lit.Kind == token.STRING {
			s, _ := strconv.Unquote(lit.Value)
			return s
		}
		return lit.Value
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func stringLit(e ast.Expr) (string, bool) {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	return "", false
}
