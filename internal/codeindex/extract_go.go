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
	receiverIdent := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = "method"
		container = receiverTypeName(d.Recv.List[0])
		// Track the receiver variable name so calls like `e.foo()`
		// resolve to file::Container.foo.
		if len(d.Recv.List[0].Names) > 0 && d.Recv.List[0].Names[0] != nil {
			receiverIdent = d.Recv.List[0].Names[0].Name
		}
	}
	startLine := fset.Position(d.Pos()).Line
	endLine := fset.Position(d.End()).Line
	sig := goFuncSignature(fset, d)

	callerQName := makeQName(fe.RelPath, container, name)
	out.Symbols = append(out.Symbols, Symbol{
		QName:     callerQName,
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

	// Walk the body for CallExpr to populate the calls table. Skip
	// builtin "len/cap/make/new/append/copy/panic/recover" since they
	// add noise without informative edges.
	if d.Body == nil {
		return
	}
	ast.Inspect(d.Body, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, calleeQ := resolveGoCallee(ce.Fun, fe.RelPath, container, receiverIdent)
		if callee == "" || isGoBuiltin(callee) {
			return true
		}
		out.Calls = append(out.Calls, ExtractedCall{
			CallerQName: callerQName,
			CalleeQName: calleeQ,
			CalleeName:  callee,
			Line:        fset.Position(ce.Lparen).Line,
		})
		return true
	})
}

// resolveGoCallee inspects a CallExpr's Fun and returns (name, qname).
// qname is non-empty only when we can resolve the call against the same
// file/container; cross-package calls leave qname blank and let the
// query side match by callee_name.
func resolveGoCallee(fun ast.Expr, file, ownContainer, recvIdent string) (name, qname string) {
	switch f := fun.(type) {
	case *ast.Ident:
		// Bare call: foo()
		// Same-file resolution — foo() usually resolves to the same
		// package. Fall back to name-only matching at query time.
		return f.Name, ""
	case *ast.SelectorExpr:
		// Selector form: X.name(...)
		sel := f.Sel.Name
		// Method on own receiver — resolve to file::ownContainer.sel
		if recvIdent != "" {
			if id, ok := f.X.(*ast.Ident); ok && id.Name == recvIdent && ownContainer != "" {
				return sel, makeQName(file, ownContainer, sel)
			}
		}
		return sel, ""
	case *ast.IndexExpr:
		return resolveGoCallee(f.X, file, ownContainer, recvIdent)
	case *ast.IndexListExpr:
		return resolveGoCallee(f.X, file, ownContainer, recvIdent)
	case *ast.ParenExpr:
		return resolveGoCallee(f.X, file, ownContainer, recvIdent)
	case *ast.FuncLit:
		// Anonymous function called inline; not a useful edge.
		return "", ""
	}
	return "", ""
}

// isGoBuiltin filters out noisy builtin "calls" that aren't real
// edges in the call graph.
func isGoBuiltin(name string) bool {
	switch name {
	case "len", "cap", "make", "new", "append", "copy",
		"panic", "recover", "print", "println",
		"close", "delete", "complex", "real", "imag":
		return true
	}
	return false
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
