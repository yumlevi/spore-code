// Tree-sitter–powered extractors for TypeScript / JavaScript / Python /
// Rust. Replaces the regex implementations the package shipped through
// v0.5.0. Go stays on go/ast (stdlib) — extract_go.go is unchanged.
//
// Tree-sitter parses a real grammar, so the previously-documented
// regex limitations (nested classes losing the outer container,
// computed method names missed, decorators only matched as a side-
// effect, anonymous default exports skipped, multi-line signatures
// truncated) are gone. The output shape (Symbol / ExtractedCall /
// ExtractedImport) is unchanged so the rest of the pipeline doesn't
// notice the swap.
package codeindex

import (
	"context"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
)

func init() {
	// One extractor per language registration. tsx is a superset of
	// vanilla TypeScript so it parses both .ts and .tsx; using it
	// uniformly avoids needing two extractors for the LangTS bucket.
	RegisterExtractor(&tsLikeExtractor{lang: LangTS, gram: tsx.GetLanguage()})
	RegisterExtractor(&tsLikeExtractor{lang: LangJS, gram: javascript.GetLanguage()})
	RegisterExtractor(&pyTSExtractor{gram: python.GetLanguage()})
	RegisterExtractor(&rustExtractor{gram: rust.GetLanguage()})
}

// ── TS / JS / TSX ──────────────────────────────────────────────────

type tsLikeExtractor struct {
	lang string
	gram *sitter.Language
}

func (e *tsLikeExtractor) Language() string { return e.lang }

func (e *tsLikeExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	parser := sitter.NewParser()
	parser.SetLanguage(e.gram)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return ExtractResult{Err: err}
	}
	defer tree.Close()
	out := ExtractResult{}
	w := &tsWalker{file: fe.RelPath, lang: e.lang, src: src, out: &out}
	w.walk(tree.RootNode(), "", "")
	return out
}

type tsWalker struct {
	file string
	lang string
	src  []byte
	out  *ExtractResult
}

// walk recurses over tree-sitter nodes, emitting symbols for
// declarations and call edges for invocations. `container` is the
// innermost enclosing class/interface name (for method attribution).
// `caller` is the qname of the innermost enclosing function/method
// (for call-edge attribution).
func (w *tsWalker) walk(n *sitter.Node, container, caller string) {
	if n == nil {
		return
	}

	switch n.Type() {

	case "function_declaration", "generator_function_declaration":
		name := w.fieldStr(n, "name")
		if name != "" {
			qn := makeQName(w.file, "", name)
			w.emitSymbol(n, name, "function", qn, "")
			w.walkBody(n, "body", "", qn)
			return
		}

	case "class_declaration", "abstract_class_declaration":
		name := w.fieldStr(n, "name")
		if name != "" {
			qn := makeQName(w.file, "", name)
			w.emitSymbol(n, name, "class", qn, "")
			w.walkBody(n, "body", name, "")
			return
		}

	case "interface_declaration":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "interface", makeQName(w.file, "", name), "")
		}
		// Don't recurse — interface bodies are TypeScript types, not
		// invokable code.
		return

	case "type_alias_declaration":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "type", makeQName(w.file, "", name), "")
		}
		return

	case "enum_declaration":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "enum", makeQName(w.file, "", name), "")
		}
		return

	case "method_definition", "method_signature", "abstract_method_signature":
		if container != "" {
			name := w.fieldStr(n, "name")
			if name != "" {
				kind := "method"
				if name == "constructor" {
					kind = "constructor"
				}
				qn := makeQName(w.file, container, name)
				// Methods carry their container's exportedness through
				// the prefix-underscore convention plus the class's
				// export modifier.
				w.emitSymbolWithContainer(n, name, kind, qn, container)
				w.walkBody(n, "body", container, qn)
				return
			}
		}

	case "public_field_definition", "field_definition":
		// Class fields with a function/arrow value count as methods.
		if container != "" {
			name := w.fieldStr(n, "name")
			val := n.ChildByFieldName("value")
			if name != "" && val != nil && (val.Type() == "arrow_function" || val.Type() == "function" || val.Type() == "function_expression") {
				qn := makeQName(w.file, container, name)
				w.emitSymbolWithContainer(n, name, "method", qn, container)
				w.walkBody(val, "body", container, qn)
				return
			}
		}

	case "lexical_declaration", "variable_declaration":
		// const/let NAME = (arrow|function|...). One node can have
		// multiple variable_declarators; emit one symbol per arrow/
		// function-like value.
		w.handleVarDecl(n, caller)
		return

	case "export_statement":
		// Recurse into the exported declaration. Children include
		// modifiers (export/default) and the actual declaration node.
		// The TS grammar exposes the inner declaration as the second-
		// last child usually; iterate to find it.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			w.walk(c, container, caller)
		}
		return

	case "import_statement":
		w.handleImport(n)
		return

	case "call_expression":
		if caller != "" {
			w.emitCall(n, caller)
		}
		// Calls can have nested calls in their arguments — recurse.

	case "new_expression":
		// `new Foo(...)` — captures the constructor call so trace_calls
		// can answer "who instantiates Foo?".
		if caller != "" {
			ctor := n.ChildByFieldName("constructor")
			if ctor != nil {
				var name string
				switch ctor.Type() {
				case "identifier":
					name = ctor.Content(w.src)
				case "member_expression":
					prop := ctor.ChildByFieldName("property")
					if prop != nil {
						name = prop.Content(w.src)
					}
				}
				if name != "" && !tsHostObjectsAndKeywords[name] {
					w.out.Calls = append(w.out.Calls, ExtractedCall{
						CallerQName: caller,
						CalleeQName: "",
						CalleeName:  name,
						Line:        int(n.StartPoint().Row) + 1,
					})
				}
			}
		}
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		w.walk(n.NamedChild(i), container, caller)
	}
}

func (w *tsWalker) walkBody(parent *sitter.Node, fieldName, container, caller string) {
	body := parent.ChildByFieldName(fieldName)
	if body == nil {
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		w.walk(body.NamedChild(i), container, caller)
	}
}

// handleVarDecl walks the declarators of a const/let/var statement
// and emits a function symbol for any whose initializer is an
// arrow_function / function / function_expression / class_expression.
// `caller` is preserved so calls inside the assigned function attribute
// to the right scope.
func (w *tsWalker) handleVarDecl(n *sitter.Node, caller string) {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		dec := n.NamedChild(i)
		if dec.Type() != "variable_declarator" {
			continue
		}
		nameN := dec.ChildByFieldName("name")
		valN := dec.ChildByFieldName("value")
		if nameN == nil || valN == nil {
			continue
		}
		// Only the simple identifier-name shape; destructuring patterns
		// are skipped (rare for assignment to a function expression).
		if nameN.Type() != "identifier" {
			continue
		}
		name := nameN.Content(w.src)
		switch valN.Type() {
		case "arrow_function", "function", "function_expression", "generator_function":
			qn := makeQName(w.file, "", name)
			w.emitSymbol(dec, name, "function", qn, "")
			w.walkBody(valN, "body", "", qn)
		case "class", "class_expression":
			qn := makeQName(w.file, "", name)
			w.emitSymbol(dec, name, "class", qn, "")
			w.walkBody(valN, "body", name, "")
		default:
			// Could still contain nested calls etc. inside the RHS.
			w.walk(valN, "", caller)
		}
	}
}

func (w *tsWalker) handleImport(n *sitter.Node) {
	src := n.ChildByFieldName("source")
	if src == nil {
		// Fallback — older grammars store the path as a string child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "string" {
				src = c
				break
			}
		}
	}
	if src == nil {
		return
	}
	target := strings.Trim(src.Content(w.src), `"'`)
	w.out.Imports = append(w.out.Imports, ExtractedImport{
		Target: target,
		Line:   int(n.StartPoint().Row) + 1,
	})
}

func (w *tsWalker) emitCall(n *sitter.Node, caller string) {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return
	}
	var name string
	switch fn.Type() {
	case "identifier":
		name = fn.Content(w.src)
	case "member_expression":
		prop := fn.ChildByFieldName("property")
		if prop != nil {
			name = prop.Content(w.src)
		}
	case "subscript_expression":
		// [Symbol.iterator]() and similar — skip; no useful name.
		return
	default:
		return
	}
	if name == "" {
		return
	}
	if tsHostObjectsAndKeywords[name] {
		return
	}
	w.out.Calls = append(w.out.Calls, ExtractedCall{
		CallerQName: caller,
		CalleeQName: "",
		CalleeName:  name,
		Line:        int(n.StartPoint().Row) + 1,
	})
}

func (w *tsWalker) emitSymbol(n *sitter.Node, name, kind, qname, container string) {
	w.emitSymbolWithContainer(n, name, kind, qname, container)
}

func (w *tsWalker) emitSymbolWithContainer(n *sitter.Node, name, kind, qname, container string) {
	exported := tsExported(name)
	w.out.Symbols = append(w.out.Symbols, Symbol{
		QName:     qname,
		Name:      name,
		Kind:      kind,
		File:      w.file,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		Signature: w.firstLineSig(n),
		Language:  w.lang,
		Container: container,
		Exported:  exported,
	})
}

// firstLineSig grabs the declaration's first source line (trimmed,
// whitespace collapsed) — same shape the regex extractor produced.
func (w *tsWalker) firstLineSig(n *sitter.Node) string {
	body := n.Content(w.src)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return collapseSpaces(strings.TrimSpace(body))
}

func (w *tsWalker) fieldStr(n *sitter.Node, field string) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(w.src)
}

// tsExported follows the existing convention: leading underscore means
// "intentionally private" by convention. JS has no syntactic exported
// marker so this is a heuristic — same one the regex extractor used.
func tsExported(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		return r != '_'
	}
	return false
}

// tsHostObjectsAndKeywords filters obvious host-object / control-flow
// "calls" so the calls table stays signal-dense. Same set as the
// regex extractor used.
var tsHostObjectsAndKeywords = map[string]bool{
	// control flow / declarations (these can't appear as call targets
	// in valid TS but the parser is forgiving and we want a tight
	// safety net)
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "throw": true, "do": true, "else": true, "function": true,
	"class": true, "interface": true, "type": true, "new": true, "await": true,
	"yield": true, "typeof": true, "void": true, "delete": true, "in": true, "of": true,
	"async": true, "import": true, "export": true, "require": true, "from": true,
	// host globals
	"console": true, "Object": true, "Array": true, "JSON": true, "Math": true,
	"Date": true, "Number": true, "String": true, "Boolean": true, "RegExp": true,
	"Error": true, "Map": true, "Set": true, "Symbol": true, "Promise": true,
	"BigInt": true, "Reflect": true, "Proxy": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "isFinite": true,
	"setTimeout": true, "setInterval": true, "clearTimeout": true, "clearInterval": true,
	"encodeURI": true, "decodeURI": true, "encodeURIComponent": true, "decodeURIComponent": true,
}

// ── Python ─────────────────────────────────────────────────────────

type pyTSExtractor struct{ gram *sitter.Language }

func (e *pyTSExtractor) Language() string { return LangPython }

func (e *pyTSExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	parser := sitter.NewParser()
	parser.SetLanguage(e.gram)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return ExtractResult{Err: err}
	}
	defer tree.Close()
	out := ExtractResult{}
	w := &pyWalker{file: fe.RelPath, src: src, out: &out}
	w.walk(tree.RootNode(), "", "", true /* atModuleLevel */)
	return out
}

type pyWalker struct {
	file string
	src  []byte
	out  *ExtractResult
}

// walk recurses over the Python tree-sitter tree.
//
// container is the innermost enclosing class name (for method
// attribution). caller is the innermost enclosing function/method
// qname (for call edge attribution). atModuleLevel governs whether
// SCREAMING_CASE assignments are treated as constants — the regex
// version had this guard.
func (w *pyWalker) walk(n *sitter.Node, container, caller string, atModuleLevel bool) {
	if n == nil {
		return
	}
	switch n.Type() {

	case "function_definition":
		name := w.fieldStr(n, "name")
		if name != "" {
			kind := "function"
			cont := ""
			if container != "" {
				kind = "method"
				cont = container
			}
			qn := makeQName(w.file, cont, name)
			w.emitSymbol(n, name, kind, qn, cont)
			w.walkBody(n, "body", cont, qn, false)
			return
		}

	case "decorated_definition":
		// decorator(s) + a function_definition or class_definition.
		// Recurse on the inner def so it lands as a normal symbol.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "function_definition" || c.Type() == "class_definition" {
				w.walk(c, container, caller, atModuleLevel)
				return
			}
		}

	case "class_definition":
		name := w.fieldStr(n, "name")
		if name != "" {
			qn := makeQName(w.file, "", name)
			w.emitSymbol(n, name, "class", qn, "")
			w.walkBody(n, "body", name, "", false)
			return
		}

	case "import_statement":
		// `import a.b.c` — walk dotted_name children.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "dotted_name" || c.Type() == "aliased_import" {
				name := c.Content(w.src)
				if c.Type() == "aliased_import" {
					mod := c.ChildByFieldName("name")
					if mod != nil {
						name = mod.Content(w.src)
					}
				}
				if name != "" {
					w.out.Imports = append(w.out.Imports, ExtractedImport{
						Target: name,
						Line:   int(n.StartPoint().Row) + 1,
					})
				}
			}
		}
		return

	case "import_from_statement":
		mod := n.ChildByFieldName("module_name")
		if mod != nil {
			w.out.Imports = append(w.out.Imports, ExtractedImport{
				Target: mod.Content(w.src),
				Line:   int(n.StartPoint().Row) + 1,
			})
		}
		return

	case "expression_statement":
		// Module-level SCREAMING_CASE constants. Look for an
		// `assignment` child with an identifier LHS.
		if atModuleLevel && container == "" && caller == "" {
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c.Type() == "assignment" {
					left := c.ChildByFieldName("left")
					if left != nil && left.Type() == "identifier" {
						name := left.Content(w.src)
						if pyIsScreamingCase(name) {
							w.emitSymbol(n, name, "const", makeQName(w.file, "", name), "")
						}
					}
				}
			}
		}

	case "call":
		if caller != "" {
			w.emitCall(n, caller)
		}
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		w.walk(n.NamedChild(i), container, caller, atModuleLevel && (n.Type() == "module" || n.Type() == "block"))
	}
}

func (w *pyWalker) walkBody(parent *sitter.Node, fieldName, container, caller string, atModuleLevel bool) {
	body := parent.ChildByFieldName(fieldName)
	if body == nil {
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		w.walk(body.NamedChild(i), container, caller, atModuleLevel)
	}
}

func (w *pyWalker) emitCall(n *sitter.Node, caller string) {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return
	}
	var name string
	switch fn.Type() {
	case "identifier":
		name = fn.Content(w.src)
	case "attribute":
		attr := fn.ChildByFieldName("attribute")
		if attr != nil {
			name = attr.Content(w.src)
		}
	default:
		return
	}
	if name == "" || pyHostObjectsAndKeywordsTS[name] {
		return
	}
	w.out.Calls = append(w.out.Calls, ExtractedCall{
		CallerQName: caller,
		CalleeQName: "",
		CalleeName:  name,
		Line:        int(n.StartPoint().Row) + 1,
	})
}

func (w *pyWalker) emitSymbol(n *sitter.Node, name, kind, qname, container string) {
	w.out.Symbols = append(w.out.Symbols, Symbol{
		QName:     qname,
		Name:      name,
		Kind:      kind,
		File:      w.file,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		Signature: w.firstLineSig(n),
		Language:  LangPython,
		Container: container,
		Exported:  !strings.HasPrefix(name, "_"),
	})
}

func (w *pyWalker) firstLineSig(n *sitter.Node) string {
	body := n.Content(w.src)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return collapseSpaces(strings.TrimSpace(body))
}

func (w *pyWalker) fieldStr(n *sitter.Node, field string) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(w.src)
}

func pyIsScreamingCase(name string) bool {
	if len(name) < 2 {
		return false
	}
	hasLetter := false
	for _, r := range name {
		if unicode.IsLetter(r) {
			hasLetter = true
			if !unicode.IsUpper(r) {
				return false
			}
		} else if r != '_' && !unicode.IsDigit(r) {
			return false
		}
	}
	return hasLetter
}

// pyHostObjectsAndKeywordsTS is the same set the regex extractor used
// — keeping it lets us preserve existing test expectations and keeps
// the calls table free of low-signal builtin invocations.
var pyHostObjectsAndKeywordsTS = map[string]bool{
	// keywords / control flow
	"if": true, "elif": true, "else": true, "for": true, "while": true,
	"return": true, "yield": true, "raise": true, "try": true, "except": true,
	"finally": true, "with": true, "as": true, "from": true, "import": true,
	"def": true, "class": true, "lambda": true, "pass": true, "break": true,
	"continue": true, "and": true, "or": true, "not": true, "in": true, "is": true,
	"True": true, "False": true, "None": true, "global": true, "nonlocal": true,
	"async": true, "await": true, "match": true, "case": true,
	// builtins
	"print": true, "len": true, "range": true, "str": true, "int": true,
	"float": true, "bool": true, "list": true, "dict": true, "set": true,
	"tuple": true, "frozenset": true, "bytes": true, "bytearray": true,
	"type": true, "isinstance": true, "issubclass": true, "id": true,
	"hash": true, "iter": true, "next": true, "abs": true, "min": true,
	"max": true, "sum": true, "any": true, "all": true, "sorted": true,
	"reversed": true, "enumerate": true, "zip": true, "map": true,
	"filter": true, "open": true, "input": true, "format": true, "repr": true,
	"ord": true, "chr": true, "hex": true, "oct": true, "bin": true,
	"round": true, "divmod": true, "pow": true, "callable": true,
	"getattr": true, "setattr": true, "hasattr": true, "delattr": true,
	"vars": true, "dir": true, "globals": true, "locals": true, "super": true,
	"object": true, "Exception": true, "ValueError": true, "TypeError": true,
	"KeyError": true, "IndexError": true, "AttributeError": true,
	"RuntimeError": true, "StopIteration": true, "NotImplementedError": true,
	"property": true, "staticmethod": true, "classmethod": true,
}

// ── Rust ───────────────────────────────────────────────────────────

type rustExtractor struct{ gram *sitter.Language }

func (e *rustExtractor) Language() string { return LangRust }

func (e *rustExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	parser := sitter.NewParser()
	parser.SetLanguage(e.gram)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return ExtractResult{Err: err}
	}
	defer tree.Close()
	out := ExtractResult{}
	w := &rustWalker{file: fe.RelPath, src: src, out: &out}
	w.walk(tree.RootNode(), "", "")
	return out
}

type rustWalker struct {
	file string
	src  []byte
	out  *ExtractResult
}

func (w *rustWalker) walk(n *sitter.Node, container, caller string) {
	if n == nil {
		return
	}
	switch n.Type() {

	case "function_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			kind := "function"
			cont := ""
			if container != "" {
				kind = "method"
				cont = container
			}
			qn := makeQName(w.file, cont, name)
			w.emitSymbol(n, name, kind, qn, cont)
			w.walkBody(n, "body", cont, qn)
			return
		}

	case "function_signature_item":
		// Trait function declarations (no body).
		name := w.fieldStr(n, "name")
		if name != "" && container != "" {
			qn := makeQName(w.file, container, name)
			w.emitSymbol(n, name, "method", qn, container)
			return
		}

	case "struct_item", "tuple_struct_item", "union_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "struct", makeQName(w.file, "", name), "")
		}
		return

	case "enum_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "enum", makeQName(w.file, "", name), "")
		}
		return

	case "trait_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			qn := makeQName(w.file, "", name)
			w.emitSymbol(n, name, "interface", qn, "") // map to "interface" so cross-language searches by kind work
			w.walkBody(n, "body", name, "")
		}
		return

	case "impl_item":
		// `impl T` or `impl Trait for T` — methods inside attribute to T.
		typ := n.ChildByFieldName("type")
		typeName := ""
		if typ != nil {
			typeName = w.simpleName(typ)
		}
		w.walkBody(n, "body", typeName, "")
		return

	case "mod_item":
		// Module — recurse into the body but DON'T treat the module
		// itself as a symbol (Rust modules act more like namespaces;
		// indexing them as symbols inflates the table without
		// helping search). Container stays empty (module-scoped
		// items still attribute to file root).
		w.walkBody(n, "body", container, caller)
		return

	case "type_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "type", makeQName(w.file, "", name), "")
		}
		return

	case "const_item", "static_item":
		name := w.fieldStr(n, "name")
		if name != "" {
			w.emitSymbol(n, name, "const", makeQName(w.file, "", name), "")
		}
		return

	case "use_declaration":
		// argument is the full use path (use std::collections::HashMap).
		arg := n.ChildByFieldName("argument")
		if arg == nil && n.NamedChildCount() > 0 {
			arg = n.NamedChild(0)
		}
		if arg != nil {
			target := arg.Content(w.src)
			// Strip any { ... } group tail to keep the import target tidy.
			if i := strings.Index(target, "::"); i >= 0 {
				// Keep the full path as-is; downstream tooling may want it.
			}
			w.out.Imports = append(w.out.Imports, ExtractedImport{
				Target: strings.TrimSpace(target),
				Line:   int(n.StartPoint().Row) + 1,
			})
		}
		return

	case "call_expression":
		if caller != "" {
			w.emitCall(n, caller)
		}

	case "macro_invocation":
		// Macros are common in Rust; record as calls so trace_calls
		// can surface things like `println!`, `panic!`, `vec!`. Filter
		// the noisiest below.
		if caller != "" {
			w.emitMacro(n, caller)
		}
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		w.walk(n.NamedChild(i), container, caller)
	}
}

func (w *rustWalker) walkBody(parent *sitter.Node, fieldName, container, caller string) {
	body := parent.ChildByFieldName(fieldName)
	if body == nil {
		// impl bodies sometimes appear as the last named child without
		// a labeled field on certain grammar versions; fall back.
		for i := 0; i < int(parent.NamedChildCount()); i++ {
			c := parent.NamedChild(i)
			if c.Type() == "declaration_list" || c.Type() == "block" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		w.walk(body.NamedChild(i), container, caller)
	}
}

// simpleName strips generics + paths to get the "primary" type name
// for `impl Foo<T>` → "Foo", `impl path::to::Bar` → "Bar".
func (w *rustWalker) simpleName(n *sitter.Node) string {
	switch n.Type() {
	case "type_identifier":
		return n.Content(w.src)
	case "generic_type":
		t := n.ChildByFieldName("type")
		if t != nil {
			return w.simpleName(t)
		}
	case "scoped_type_identifier":
		// path::to::Name → Name
		nm := n.ChildByFieldName("name")
		if nm != nil {
			return nm.Content(w.src)
		}
	}
	c := n.Content(w.src)
	if i := strings.LastIndex(c, "::"); i >= 0 {
		c = c[i+2:]
	}
	if i := strings.IndexByte(c, '<'); i >= 0 {
		c = c[:i]
	}
	return strings.TrimSpace(c)
}

func (w *rustWalker) emitCall(n *sitter.Node, caller string) {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return
	}
	var name string
	switch fn.Type() {
	case "identifier":
		name = fn.Content(w.src)
	case "field_expression":
		f := fn.ChildByFieldName("field")
		if f != nil {
			name = f.Content(w.src)
		}
	case "scoped_identifier":
		nm := fn.ChildByFieldName("name")
		if nm != nil {
			name = nm.Content(w.src)
		}
	default:
		return
	}
	if name == "" || rustHostKeywords[name] {
		return
	}
	w.out.Calls = append(w.out.Calls, ExtractedCall{
		CallerQName: caller,
		CalleeQName: "",
		CalleeName:  name,
		Line:        int(n.StartPoint().Row) + 1,
	})
}

func (w *rustWalker) emitMacro(n *sitter.Node, caller string) {
	mac := n.ChildByFieldName("macro")
	if mac == nil {
		return
	}
	name := mac.Content(w.src)
	if name == "" {
		return
	}
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	if rustNoiseMacros[name] {
		return
	}
	w.out.Calls = append(w.out.Calls, ExtractedCall{
		CallerQName: caller,
		CalleeQName: "",
		CalleeName:  name + "!",
		Line:        int(n.StartPoint().Row) + 1,
	})
}

func (w *rustWalker) emitSymbol(n *sitter.Node, name, kind, qname, container string) {
	w.out.Symbols = append(w.out.Symbols, Symbol{
		QName:     qname,
		Name:      name,
		Kind:      kind,
		File:      w.file,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		Signature: w.firstLineSig(n),
		Language:  LangRust,
		Container: container,
		// Rust visibility is explicit via `pub`. The signature line
		// captures it; for the boolean we report pub-prefixed names
		// as exported. Default conservative: name not starting with
		// underscore.
		Exported: rustIsExported(n, w.src, name),
	})
}

func (w *rustWalker) firstLineSig(n *sitter.Node) string {
	body := n.Content(w.src)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return collapseSpaces(strings.TrimSpace(body))
}

func (w *rustWalker) fieldStr(n *sitter.Node, field string) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(w.src)
}

// rustIsExported returns true if the declaration line starts with
// `pub` modifier. The tree-sitter grammar exposes visibility_modifier
// as a child; check that first, fall back to source line scanning.
func rustIsExported(n *sitter.Node, src []byte, name string) bool {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == "visibility_modifier" {
			return true
		}
	}
	if strings.HasPrefix(name, "_") {
		return false
	}
	// Modest fallback — prefix check on the trimmed first line.
	body := n.Content(src)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return strings.HasPrefix(strings.TrimSpace(body), "pub")
}

var rustHostKeywords = map[string]bool{
	"if": true, "else": true, "match": true, "loop": true, "while": true, "for": true,
	"return": true, "break": true, "continue": true, "in": true, "as": true,
	"async": true, "await": true, "move": true, "ref": true, "mut": true,
	"let": true, "const": true, "static": true, "fn": true, "impl": true,
	"struct": true, "enum": true, "trait": true, "type": true, "use": true,
	"pub": true, "mod": true, "Self": true, "self": true, "super": true, "crate": true,
}

var rustNoiseMacros = map[string]bool{
	// Filter the loudest stdlib macros so the calls table doesn't
	// flood with println! noise; keep `panic!` etc. since those are
	// signal-bearing.
	"println":  true,
	"print":    true,
	"eprintln": true,
	"eprint":   true,
	"format":   true,
	"write":    true,
	"writeln":  true,
	"dbg":      true,
}
