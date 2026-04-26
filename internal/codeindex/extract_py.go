package codeindex

import (
	"regexp"
	"strings"
)

func init() { RegisterExtractor(pyExtractor{}) }

// pyExtractor handles Python via regex + indent-tracking. Same shape as
// the TS extractor: function-frame stack for call attribution, but
// Python uses indentation instead of braces so we track the indent
// level of each class/def header rather than counting `{`/`}`.
//
// Known limitations (documented for the agent prompt):
//   - decorated symbols are still found because we match the def/class
//     line itself, not the decorator
//   - inline lambda / nested-def calls attribute to the outermost
//     containing def
//   - module-level "constants" only matched on SCREAMING_CASE names
//     (the rest of module-level code is too noisy to capture as
//     symbols without an import-aware analyzer)
//   - triple-quoted strings tracked by simple presence-of-delimiter;
//     adjacent triple-quotes on the same line aren't perfectly handled
type pyExtractor struct{}

func (pyExtractor) Language() string { return LangPython }

// Top-level patterns. Captures: (1) leading whitespace, (2) name.
var rePyDef = regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+([A-Za-z_][\w]*)\s*\(`)
var rePyClass = regexp.MustCompile(`^(\s*)class\s+([A-Za-z_][\w]*)\s*[(:]`)
var rePyImportFrom = regexp.MustCompile(`^\s*from\s+(\S+)\s+import\s+`)
var rePyImport = regexp.MustCompile(`^\s*import\s+([A-Za-z_][\w.]*)`)

// SCREAMING_CASE module-level constant only — anything else at module
// level is too noisy without semantic analysis.
var rePyConst = regexp.MustCompile(`^([A-Z][A-Z0-9_]+)\s*[:=]`)

// Call site regex — same shape as TS but Python-friendly identifier set.
// Captures optional preceding `.` so both bare and method-style calls
// land as edges (resolved by callee_name at query time).
var reCallSitePy = regexp.MustCompile(`(\.\s*)?([A-Za-z_][\w]*)\s*\(`)

// pyHostObjectsAndKeywords filters out names that are Python keywords
// or builtins, which would otherwise add noise to the calls table.
var pyHostObjectsAndKeywords = map[string]bool{
	// keywords / control flow
	"if": true, "elif": true, "else": true, "for": true, "while": true,
	"return": true, "yield": true, "raise": true, "try": true, "except": true,
	"finally": true, "with": true, "as": true, "from": true, "import": true,
	"def": true, "class": true, "lambda": true, "pass": true, "break": true,
	"continue": true, "and": true, "or": true, "not": true, "in": true, "is": true,
	"True": true, "False": true, "None": true, "global": true, "nonlocal": true,
	"async": true, "await": true, "match": true, "case": true,
	// common builtins (non-exhaustive — focuses on the noisiest)
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

// pyIndentLevel counts leading whitespace; tabs count as 4 spaces (PEP
// 8 convention). Mixed indentation is rare in modern code; if a file
// mixes them, the count is approximate but the relative ordering of
// indents stays correct.
func pyIndentLevel(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func (pyExtractor) Extract(fe FileEntry, src []byte) ExtractResult {
	out := ExtractResult{}
	lines := strings.Split(string(src), "\n")

	// Class stack — each entry's headerIndent is the indent of the
	// `class Foo:` line. Anything at indent > headerIndent is inside
	// the class body. When we see a non-blank line at indent <=
	// headerIndent, we pop the class.
	type classFrame struct {
		name         string
		headerIndent int
	}
	var stack []classFrame

	// Function/method frame stack — same shape as classFrame but used
	// to attribute call edges to the innermost containing def.
	type funcFrame struct {
		callerQName  string
		headerIndent int
	}
	var fstack []funcFrame

	// Multi-line string state. Naive: tracks the active triple-quote
	// kind (""" or '''). Lines inside the string are skipped wholesale.
	inDocstring := false
	docstringQuote := ""

	for i, raw := range lines {
		lineNo := i + 1

		if inDocstring {
			if strings.Contains(raw, docstringQuote) {
				inDocstring = false
			}
			continue
		}

		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect a triple-quoted string opener that doesn't close on the
		// same line — we then enter multi-line skip mode.
		if open, q, single := pyDetectTripleQuote(trimmed); open && !single {
			inDocstring = true
			docstringQuote = q
			continue
		}

		ind := pyIndentLevel(raw)

		// Pop class/function frames whose body has ended. A frame at
		// headerIndent N covers all lines with indent > N.
		for len(stack) > 0 && ind <= stack[len(stack)-1].headerIndent {
			stack = stack[:len(stack)-1]
		}
		for len(fstack) > 0 && ind <= fstack[len(fstack)-1].headerIndent {
			fstack = fstack[:len(fstack)-1]
		}

		// class header
		if m := rePyClass.FindStringSubmatch(raw); m != nil {
			name := m[2]
			container := ""
			if len(stack) > 0 {
				container = stack[len(stack)-1].name
			}
			qn := makeQName(fe.RelPath, container, name)
			out.Symbols = append(out.Symbols, Symbol{
				QName:     qn,
				Name:      name,
				Kind:      "class",
				File:      fe.RelPath,
				StartLine: lineNo,
				EndLine:   lineNo,
				Signature: collapseSpaces(strings.TrimSpace(raw)),
				Language:  LangPython,
				Container: container,
				Exported:  !strings.HasPrefix(name, "_"),
			})
			stack = append(stack, classFrame{name: name, headerIndent: ind})
			continue
		}

		// def header (function or method)
		if m := rePyDef.FindStringSubmatch(raw); m != nil {
			name := m[2]
			kind := "function"
			container := ""
			if len(stack) > 0 && ind > stack[len(stack)-1].headerIndent {
				kind = "method"
				container = stack[len(stack)-1].name
			}
			qn := makeQName(fe.RelPath, container, name)
			out.Symbols = append(out.Symbols, Symbol{
				QName:     qn,
				Name:      name,
				Kind:      kind,
				File:      fe.RelPath,
				StartLine: lineNo,
				EndLine:   lineNo,
				Signature: collapseSpaces(strings.TrimSpace(raw)),
				Language:  LangPython,
				Container: container,
				Exported:  !strings.HasPrefix(name, "_"),
			})
			fstack = append(fstack, funcFrame{callerQName: qn, headerIndent: ind})
			continue
		}

		// Module-level SCREAMING_CASE constants.
		if ind == 0 && len(stack) == 0 && len(fstack) == 0 {
			if m := rePyConst.FindStringSubmatch(raw); m != nil {
				name := m[1]
				out.Symbols = append(out.Symbols, Symbol{
					QName:     makeQName(fe.RelPath, "", name),
					Name:      name,
					Kind:      "const",
					File:      fe.RelPath,
					StartLine: lineNo,
					EndLine:   lineNo,
					Signature: collapseSpaces(strings.TrimSpace(raw)),
					Language:  LangPython,
					Exported:  true,
				})
			}
		}

		// imports — `from x import y`  or  `import a.b.c`
		if m := rePyImportFrom.FindStringSubmatch(raw); m != nil {
			out.Imports = append(out.Imports, ExtractedImport{Target: m[1], Line: lineNo})
		} else if m := rePyImport.FindStringSubmatch(raw); m != nil {
			out.Imports = append(out.Imports, ExtractedImport{Target: m[1], Line: lineNo})
		}

		// Call extraction inside function bodies. The TS extractor's
		// stripStringsAndLineComments doesn't know about Python's `#`
		// comment marker — use a Python-aware version.
		if len(fstack) > 0 {
			top := fstack[len(fstack)-1]
			stripped := pyStripStringsAndComments(raw)
			for _, m := range reCallSitePy.FindAllStringSubmatchIndex(stripped, -1) {
				nameStart, nameEnd := m[4], m[5]
				if nameStart < 0 {
					continue
				}
				name := stripped[nameStart:nameEnd]
				if pyHostObjectsAndKeywords[name] {
					continue
				}
				out.Calls = append(out.Calls, ExtractedCall{
					CallerQName: top.callerQName,
					CalleeQName: "", // regex layer can't resolve cross-module
					CalleeName:  name,
					Line:        lineNo,
				})
			}
		}
	}
	return out
}

// pyDetectTripleQuote inspects a stripped (TrimSpace'd) line and reports:
//   - open: true when the line opens a triple-quoted string
//   - q:    """ or '''
//   - single: true when the same triple-quote also closes on this line
//
// Only used for multi-line docstring tracking; single-line triple-quoted
// strings (ALSO closed on the same line) don't put us in skip mode.
func pyDetectTripleQuote(line string) (open bool, q string, single bool) {
	for _, candidate := range []string{`"""`, `'''`} {
		if strings.HasPrefix(line, candidate) {
			rest := strings.TrimPrefix(line, candidate)
			if strings.Contains(rest, candidate) {
				return true, candidate, true
			}
			return true, candidate, false
		}
	}
	return false, "", false
}

// pyStripStringsAndComments produces a copy of `line` with string
// content and `#`-comments replaced with spaces. Used for safer call
// extraction. Naive — doesn't handle multi-line strings (those are
// filtered upstream by the docstring state machine).
func pyStripStringsAndComments(line string) string {
	out := make([]byte, len(line))
	copy(out, line)

	i := 0
	for i < len(out) {
		c := out[i]
		// Comment to EOL
		if c == '#' {
			for j := i; j < len(out); j++ {
				out[j] = ' '
			}
			break
		}
		// String literal — handles ", ', f-strings, b-strings, etc.
		if c == '"' || c == '\'' {
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
			i = j + 1
			continue
		}
		i++
	}
	return string(out)
}
