package codeindex

import (
	"regexp"
	"strings"
	"unicode"
)

func init() {
	RegisterExtractor(tsExtractor{lang: LangTS})
	RegisterExtractor(tsExtractor{lang: LangJS})
}

// tsExtractor handles TypeScript and JavaScript via regex + brace
// tracking. Tree-sitter precision is the planned upgrade (gated behind
// a build tag because cgo); the v1 regex form catches the standard
// declaration shapes well enough to power search_symbols.
//
// Known limitations (documented for the agent prompt):
//   - decorated declarations (`@decorator class Foo`) → still found
//     because we match the line containing `class Foo`
//   - inline object methods (`{ foo() {} }` outside a class) → missed
//   - nested classes deeper than 1 level → outer container loses track
//   - JSX/TSX tag content is treated as code; spurious matches are rare
//     but possible
type tsExtractor struct{ lang string }

func (e tsExtractor) Language() string { return e.lang }

// Pattern: top-level function declarations.
//   [export] [default] [async] function NAME(
// Capture NAME and whether export was present.
var reTopFunc = regexp.MustCompile(
	`^\s*(?:export\s+(?:default\s+)?)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*[(<]`,
)

// Pattern: const/let/var assigned to an arrow or function expression.
//   [export] (const|let|var) NAME = [async] (params) => ...
//   [export] (const|let|var) NAME = [async] function ...
var reTopConstFunc = regexp.MustCompile(
	`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::\s*[^=]+)?\s*=\s*(?:async\s+)?(?:\([^)]*\)|function(?:\s+[A-Za-z_$][\w$]*)?|[A-Za-z_$][\w$]*)\s*(?:=>|\()`,
)

// Pattern: class declaration.
//   [export] [default] (abstract )?class NAME ...
var reClass = regexp.MustCompile(
	`^\s*(?:export\s+(?:default\s+)?)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`,
)

// Pattern: interface (TS only).
//   [export] interface NAME ...
var reInterface = regexp.MustCompile(
	`^\s*(?:export\s+)?interface\s+([A-Za-z_$][\w$]*)`,
)

// Pattern: type alias (TS only).
//   [export] type NAME = ...
var reTypeAlias = regexp.MustCompile(
	`^\s*(?:export\s+)?type\s+([A-Za-z_$][\w$]*)\s*=`,
)

// Pattern: enum (TS only).
//   [export] [const] enum NAME ...
var reEnum = regexp.MustCompile(
	`^\s*(?:export\s+)?(?:const\s+)?enum\s+([A-Za-z_$][\w$]*)`,
)

// Pattern: class method or accessor.
//   [public/private/protected/static/readonly/async/get/set/override] NAME(
// Reject control-flow keywords and obvious non-method matches.
var reClassMember = regexp.MustCompile(
	`^\s*(?:(?:public|private|protected|static|readonly|abstract|override|async|get|set)\s+)*([A-Za-z_$][\w$]*)\s*[(<]`,
)

// Words that look like methods to the regex but aren't (control flow,
// loops, the constructor synonyms we skip via separate handling).
var notAMethodKeyword = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "throw": true, "do": true, "else": true,
	"function": true, "class": true, "interface": true, "type": true,
	"new": true, "await": true, "yield": true, "typeof": true, "void": true,
	"delete": true, "in": true, "of": true,
}

// blockCommentRE strips /* ... */ across lines (replacing with spaces of
// matching length to preserve line numbers).
var blockCommentRE = regexp.MustCompile(`/\*[\s\S]*?\*/`)

// reCallSite matches `name(` invocations. The optional `.` lets us
// capture both bare calls (`foo()`) and method calls (`obj.foo()`); we
// resolve the latter to a name-only edge since the receiver is opaque
// at the regex layer.
var reCallSite = regexp.MustCompile(`(\.\s*)?([A-Za-z_$][\w$]*)\s*\(`)

// hostObjectsAndKeywords are names that, when seen in `name(` form,
// almost always represent JS/TS builtin calls or control flow rather
// than user-graph edges. Filtered out to keep the calls table signal-
// dense. Method-style invocations (`.log(`) are included via the
// callee_name column for trace_calls "find callers of log".
var hostObjectsAndKeywords = map[string]bool{
	// control flow / declarations
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "throw": true, "do": true, "else": true, "function": true,
	"class": true, "interface": true, "type": true, "new": true, "await": true,
	"yield": true, "typeof": true, "void": true, "delete": true, "in": true, "of": true,
	"async": true, "import": true, "export": true, "require": true, "from": true,
	// host globals (very common, low signal as call-graph edges)
	"console": true, "Object": true, "Array": true, "JSON": true, "Math": true,
	"Date": true, "Number": true, "String": true, "Boolean": true, "RegExp": true,
	"Error": true, "Map": true, "Set": true, "Symbol": true, "Promise": true,
	"BigInt": true, "Reflect": true, "Proxy": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "isFinite": true,
	"setTimeout": true, "setInterval": true, "clearTimeout": true, "clearInterval": true,
	"encodeURI": true, "decodeURI": true, "encodeURIComponent": true, "decodeURIComponent": true,
}

// Extract walks the file line by line, tracking class-body depth so
// methods get attributed to the right container.
func (e tsExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	out := ExtractResult{}

	// Strip block comments first to avoid false positives.
	cleaned := blockCommentRE.ReplaceAllStringFunc(string(src), func(m string) string {
		// Preserve newlines for accurate line numbers.
		var sb strings.Builder
		for _, r := range m {
			if r == '\n' {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(' ')
			}
		}
		return sb.String()
	})

	lines := strings.Split(cleaned, "\n")

	// Class stack — each entry is the brace depth at which the class
	// opened. We track only the innermost class for container naming;
	// nested classes are flagged via the stack length but the
	// outer container is lost beyond depth 1 (documented limitation).
	type classFrame struct {
		name      string
		startLine int
		exported  bool
		openDepth int
	}
	var stack []classFrame

	// Function/method frame stack — used to attribute regex-detected
	// calls to the innermost containing function/method. Pushed when
	// a function declaration or class member is recognized AND its
	// body opens with `{`. Popped when depth drops below openDepth.
	type funcFrame struct {
		callerQName string
		openDepth   int
	}
	var fstack []funcFrame

	depth := 0
	exportedNext := false

	for i, raw := range lines {
		lineNo := i + 1
		// Strip line comment + simple string content for brace counting.
		stripped := stripStringsAndLineComments(raw)

		// Cheap: try top-level declarations only when we're outside any class.
		if len(stack) == 0 {
			if m := reTopFunc.FindStringSubmatch(raw); m != nil {
				name := m[1]
				qn := makeQName(fe.RelPath, "", name)
				out.Symbols = append(out.Symbols, Symbol{
					QName:     qn,
					Name:      name,
					Kind:      "function",
					File:      fe.RelPath,
					StartLine: lineNo,
					EndLine:   lineNo,
					Signature: collapseSpaces(strings.TrimSpace(raw)),
					Language:  e.lang,
					Exported:  strings.Contains(raw, "export "),
				})
				if strings.Contains(stripped, "{") {
					fstack = append(fstack, funcFrame{callerQName: qn, openDepth: depth})
				} else {
					fstack = append(fstack, funcFrame{callerQName: qn, openDepth: -1})
				}
			} else if m := reTopConstFunc.FindStringSubmatch(raw); m != nil {
				name := m[1]
				out.Symbols = append(out.Symbols, Symbol{
					QName:     makeQName(fe.RelPath, "", name),
					Name:      name,
					Kind:      "function",
					File:      fe.RelPath,
					StartLine: lineNo,
					EndLine:   lineNo,
					Signature: collapseSpaces(strings.TrimSpace(raw)),
					Language:  e.lang,
					Exported:  strings.HasPrefix(strings.TrimSpace(raw), "export"),
				})
			} else if m := reClass.FindStringSubmatch(raw); m != nil {
				name := m[1]
				exported := strings.Contains(raw, "export ")
				out.Symbols = append(out.Symbols, Symbol{
					QName:     makeQName(fe.RelPath, "", name),
					Name:      name,
					Kind:      "class",
					File:      fe.RelPath,
					StartLine: lineNo,
					EndLine:   lineNo,
					Signature: collapseSpaces(strings.TrimSpace(raw)),
					Language:  e.lang,
					Exported:  exported,
				})
				// Push class frame when we actually see the opening brace.
				exportedNext = exported
				if strings.Contains(stripped, "{") {
					stack = append(stack, classFrame{
						name: name, startLine: lineNo, exported: exported, openDepth: depth,
					})
					exportedNext = false
				} else {
					// Class body opens on a later line; record the pending name.
					stack = append(stack, classFrame{
						name: name, startLine: lineNo, exported: exported, openDepth: -1,
					})
				}
			} else if e.lang == LangTS {
				if m := reInterface.FindStringSubmatch(raw); m != nil {
					name := m[1]
					out.Symbols = append(out.Symbols, Symbol{
						QName:     makeQName(fe.RelPath, "", name),
						Name:      name,
						Kind:      "interface",
						File:      fe.RelPath,
						StartLine: lineNo,
						EndLine:   lineNo,
						Signature: collapseSpaces(strings.TrimSpace(raw)),
						Language:  e.lang,
						Exported:  strings.Contains(raw, "export "),
					})
				} else if m := reTypeAlias.FindStringSubmatch(raw); m != nil {
					name := m[1]
					out.Symbols = append(out.Symbols, Symbol{
						QName:     makeQName(fe.RelPath, "", name),
						Name:      name,
						Kind:      "type",
						File:      fe.RelPath,
						StartLine: lineNo,
						EndLine:   lineNo,
						Signature: collapseSpaces(strings.TrimSpace(raw)),
						Language:  e.lang,
						Exported:  strings.Contains(raw, "export "),
					})
				} else if m := reEnum.FindStringSubmatch(raw); m != nil {
					name := m[1]
					out.Symbols = append(out.Symbols, Symbol{
						QName:     makeQName(fe.RelPath, "", name),
						Name:      name,
						Kind:      "enum",
						File:      fe.RelPath,
						StartLine: lineNo,
						EndLine:   lineNo,
						Signature: collapseSpaces(strings.TrimSpace(raw)),
						Language:  e.lang,
						Exported:  strings.Contains(raw, "export "),
					})
				}
			}
		} else if len(stack) > 0 {
			// Inside class — try class member regex, but only at depth == class+1.
			top := &stack[len(stack)-1]
			if top.openDepth >= 0 && depth == top.openDepth+1 {
				if m := reClassMember.FindStringSubmatch(raw); m != nil {
					name := m[1]
					if !notAMethodKeyword[name] && !looksLikeAssignment(raw, m[0]) {
						kind := "method"
						if name == "constructor" {
							kind = "constructor"
						}
						qn := makeQName(fe.RelPath, top.name, name)
						out.Symbols = append(out.Symbols, Symbol{
							QName:     qn,
							Name:      name,
							Kind:      kind,
							File:      fe.RelPath,
							StartLine: lineNo,
							EndLine:   lineNo,
							Signature: collapseSpaces(strings.TrimSpace(raw)),
							Language:  e.lang,
							Container: top.name,
							Exported:  isExportedJS(name),
						})
						// Push function frame so calls in the body
						// attribute back to this method.
						if strings.Contains(stripped, "{") {
							fstack = append(fstack, funcFrame{callerQName: qn, openDepth: depth})
						} else {
							fstack = append(fstack, funcFrame{callerQName: qn, openDepth: -1})
						}
					}
				}
			}
		}

		// Call extraction — only attempted while we're inside a known
		// function body (innermost frame on fstack). Suppresses the
		// declaration line of the function itself by checking that
		// at least one frame is fully open at this depth.
		if len(fstack) > 0 {
			top := fstack[len(fstack)-1]
			if top.openDepth >= 0 && depth > top.openDepth {
				for _, m := range reCallSite.FindAllStringSubmatchIndex(stripped, -1) {
					// m: [matchStart, matchEnd, dotStart, dotEnd, nameStart, nameEnd]
					nameStart, nameEnd := m[4], m[5]
					if nameStart < 0 {
						continue
					}
					name := stripped[nameStart:nameEnd]
					if hostObjectsAndKeywords[name] {
						continue
					}
					out.Calls = append(out.Calls, ExtractedCall{
						CallerQName: top.callerQName,
						CalleeQName: "", // regex layer can't resolve; query side matches by callee_name
						CalleeName:  name,
						Line:        lineNo,
					})
				}
			}
		}

		// Brace counting. Increment first (a class header line containing
		// `{` opens its body at the new depth).
		opens, closes := countBraces(stripped)
		// If the topmost class frame was waiting for its `{` and we just saw
		// one, set its openDepth.
		if len(stack) > 0 && stack[len(stack)-1].openDepth < 0 && opens > 0 {
			stack[len(stack)-1].openDepth = depth
			_ = exportedNext // consumed
		}
		// Same treatment for the topmost function frame.
		if len(fstack) > 0 && fstack[len(fstack)-1].openDepth < 0 && opens > 0 {
			fstack[len(fstack)-1].openDepth = depth
		}
		depth += opens
		depth -= closes
		// Pop class frames whose body has closed.
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			if f.openDepth >= 0 && depth <= f.openDepth {
				stack = stack[:len(stack)-1]
				continue
			}
			break
		}
		// Pop function frames whose body has closed.
		for len(fstack) > 0 {
			f := fstack[len(fstack)-1]
			if f.openDepth >= 0 && depth <= f.openDepth {
				fstack = fstack[:len(fstack)-1]
				continue
			}
			break
		}
		if depth < 0 {
			depth = 0
		}
	}

	return out
}

// countBraces returns the number of `{` and `}` in the given string,
// which the caller has already passed through stripStringsAndLineComments.
func countBraces(s string) (opens, closes int) {
	for _, r := range s {
		switch r {
		case '{':
			opens++
		case '}':
			closes++
		}
	}
	return
}

// stripStringsAndLineComments produces a version of one line with quoted
// string contents and `//`-style trailing comments replaced by spaces,
// preserving column positions. Used for brace counting only.
func stripStringsAndLineComments(line string) string {
	out := make([]byte, len(line))
	copy(out, line)

	i := 0
	for i < len(out) {
		c := out[i]
		// Line comment — wipe to EOL.
		if c == '/' && i+1 < len(out) && out[i+1] == '/' {
			for j := i; j < len(out); j++ {
				out[j] = ' '
			}
			break
		}
		// String literals: ", ', `
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			j := i + 1
			for j < len(out) {
				if out[j] == '\\' && j+1 < len(out) {
					out[j] = ' '
					out[j+1] = ' '
					j += 2
					continue
				}
				if out[j] == quote {
					break
				}
				out[j] = ' '
				j++
			}
			if j < len(out) {
				// keep the closing quote in place; nothing to do
			}
			i = j + 1
			continue
		}
		i++
	}
	return string(out)
}

// looksLikeAssignment guards against false positives where the regex
// matches "foo = bar(baz)" or "foo: type = bar(baz)" — those are
// property assignments, not method declarations. Method declarations
// have an opening paren immediately after the name (modulo type params).
func looksLikeAssignment(line, prefix string) bool {
	rest := strings.TrimPrefix(strings.TrimLeft(line, " \t"), strings.TrimLeft(prefix, " \t"))
	rest = strings.TrimSpace(rest)
	// If the matched prefix already ended at `(` or `<`, the regex
	// captured the method form. Anything else is suspect.
	return !strings.HasSuffix(strings.TrimRight(prefix, " \t"), "(") &&
		!strings.HasSuffix(strings.TrimRight(prefix, " \t"), "<")
}

// isExportedJS — JS doesn't have a syntactic exported convention like
// Go's leading-uppercase rule. Conservative default: true unless the
// name leads with `_` (project convention for private members).
func isExportedJS(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		return r != '_' && (unicode.IsLetter(r) || r == '$')
	}
	return false
}
