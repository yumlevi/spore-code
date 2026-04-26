package codeindex

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"unicode"
)

func init() { RegisterExtractor(goExtractor{}) }

// goExtractor uses go/parser + go/ast (stdlib, pure Go). Symbols
// extracted: function, method, type, struct, interface, const, var.
// Calls land in M2.
type goExtractor struct{}

func (goExtractor) Language() string { return LangGo }

func (goExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	fset := token.NewFileSet()
	// SkipObjectResolution makes parsing ~30% faster and we don't need
	// the resolved-object metadata for symbol extraction.
	f, err := parser.ParseFile(fset, fe.AbsPath, src, parser.SkipObjectResolution)
	if err != nil {
		// Best-effort: even with parse errors, ast.File may be partial.
		// Try to extract whatever the parser produced; report err so the
		// caller can log it.
		if f == nil {
			return ExtractResult{Err: err}
		}
	}
	out := ExtractResult{}

	// Imports — line per spec.
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		alias := ""
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		out.Imports = append(out.Imports, ExtractedImport{
			Target: path,
			Alias:  alias,
			Line:   fset.Position(imp.Pos()).Line,
		})
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			extractFuncDecl(fe, fset, d, &out)
		case *ast.GenDecl:
			extractGenDecl(fe, fset, d, &out)
		}
	}
	return out
}

func extractFuncDecl(fe FileEntry, fset *token.FileSet, d *ast.FuncDecl, out *ExtractResult) {
	if d.Name == nil {
		return
	}
	name := d.Name.Name
	kind := "function"
	container := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = "method"
		container = receiverTypeName(d.Recv.List[0])
	}
	startLine := fset.Position(d.Pos()).Line
	endLine := fset.Position(d.End()).Line
	sig := goFuncSignature(fset, d)
	out.Symbols = append(out.Symbols, Symbol{
		QName:     makeQName(fe.RelPath, container, name),
		Name:      name,
		Kind:      kind,
		File:      fe.RelPath,
		StartLine: startLine,
		EndLine:   endLine,
		Signature: sig,
		Language:  LangGo,
		Container: container,
		Exported:  isExportedIdent(name),
	})
}

func extractGenDecl(fe FileEntry, fset *token.FileSet, d *ast.GenDecl, out *ExtractResult) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name == nil {
				continue
			}
			name := s.Name.Name
			kind := "type"
			switch s.Type.(type) {
			case *ast.StructType:
				kind = "struct"
			case *ast.InterfaceType:
				kind = "interface"
			}
			out.Symbols = append(out.Symbols, Symbol{
				QName:     makeQName(fe.RelPath, "", name),
				Name:      name,
				Kind:      kind,
				File:      fe.RelPath,
				StartLine: fset.Position(s.Pos()).Line,
				EndLine:   fset.Position(s.End()).Line,
				Signature: goTypeSignature(fset, s),
				Language:  LangGo,
				Exported:  isExportedIdent(name),
			})
		case *ast.ValueSpec:
			kind := "const"
			if d.Tok == token.VAR {
				kind = "var"
			}
			for _, n := range s.Names {
				if n == nil || n.Name == "_" {
					continue
				}
				out.Symbols = append(out.Symbols, Symbol{
					QName:     makeQName(fe.RelPath, "", n.Name),
					Name:      n.Name,
					Kind:      kind,
					File:      fe.RelPath,
					StartLine: fset.Position(n.Pos()).Line,
					EndLine:   fset.Position(s.End()).Line,
					Signature: "",
					Language:  LangGo,
					Exported:  isExportedIdent(n.Name),
				})
			}
		}
	}
}

// receiverTypeName extracts the bare type name from a method receiver,
// stripping pointer indirection: "*Executor" -> "Executor".
func receiverTypeName(field *ast.Field) string {
	expr := field.Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	if idx, ok := expr.(*ast.IndexExpr); ok { // generic receiver
		if id, ok := idx.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	if idx, ok := expr.(*ast.IndexListExpr); ok { // multi-param generic receiver
		if id, ok := idx.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// isExportedIdent matches Go's exported convention: leading uppercase
// rune. Mirrors token.IsExported but doesn't require importing token.
func isExportedIdent(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}

// goFuncSignature renders the func declaration head (no body) by
// printing only the FuncDecl with Body cleared. Capped to one line.
func goFuncSignature(fset *token.FileSet, d *ast.FuncDecl) string {
	cp := *d
	cp.Body = nil
	var sb strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 1}
	if err := cfg.Fprint(&sb, fset, &cp); err != nil {
		return ""
	}
	s := strings.ReplaceAll(sb.String(), "\n", " ")
	return collapseSpaces(s)
}

// goTypeSignature renders a type declaration's RHS in compact form.
// Long struct/interface bodies are truncated to keep the column small.
func goTypeSignature(fset *token.FileSet, s *ast.TypeSpec) string {
	var sb strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 1}
	if err := cfg.Fprint(&sb, fset, s); err != nil {
		return ""
	}
	out := collapseSpaces(strings.ReplaceAll(sb.String(), "\n", " "))
	if len(out) > 200 {
		out = out[:200] + "…"
	}
	return out
}

func collapseSpaces(s string) string {
	var sb strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				sb.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		sb.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(sb.String())
}
