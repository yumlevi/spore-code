package tools

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yumlevi/acorn-cli/internal/codeindex"
)

// verify_implementation — goal-backward check of "is this symbol
// actually implemented" for a list of qnames or every symbol in a
// list of files. Runs four explicit levels (the GSD pattern,
// adapted for acorn's tree-sitter index):
//
//   1. **exists** — the symbol is in the index
//   2. **substantive** — body isn't a stub (TODO, pass, "not
//      implemented", single throw/raise/panic, etc.)
//   3. **wired** — at least one CALLS edge points at it
//   4. **export_level** — at least one caller is in a different
//      file (i.e. it's actually used outside its declaration site)
//
// Designed for use after a plan step claims to have created /
// modified a symbol. Returns a structured per-symbol report so the
// agent can self-correct (or PLAN_READY can be blocked) when "done"
// turns out to mean "stub".
//
// Inputs (one of these forms):
//   qnames: []string  — explicit list of qualified names
//   paths:  []string  — verify every symbol declared in these files
//
// Both can be combined; results merge.
func VerifyImplementation(input map[string]any, cwd string) any {
	refreshed, oldHead, newHead := ensureIndexFresh(cwd)
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	qnames := asStringSlice(input["qnames"])
	paths := asStringSlice(input["paths"])

	// Collect symbols to verify, deduped by qname.
	want := map[string]codeindex.Symbol{}
	for _, qn := range qnames {
		sym, err := store.GetSymbol(qn)
		if err != nil {
			continue
		}
		if sym != nil {
			want[sym.QName] = *sym
			continue
		}
		// Symbol not in index — record it as a not-found stub so the
		// agent sees the failure (rather than silently dropping).
		want[qn] = codeindex.Symbol{QName: qn}
	}
	for _, p := range paths {
		// Search index for symbols in that file (case-insensitive
		// LIKE on the path column).
		res, err := store.Search(codeindex.SearchQuery{FileLike: p, Limit: 500})
		if err != nil {
			continue
		}
		for _, s := range res {
			if _, ok := want[s.QName]; !ok {
				want[s.QName] = s
			}
		}
	}

	if len(want) == 0 {
		return map[string]any{
			"ok":    true,
			"count": 0,
			"note":  "no symbols matched the qnames or paths supplied; nothing to verify",
		}
	}

	out := make([]map[string]any, 0, len(want))
	overallPassed := 0
	overallFailed := 0
	for _, sym := range want {
		r := verifyOne(store, sym, cwd)
		if !r.exists || !r.substantive || !r.wired || !r.exportLevel {
			overallFailed++
		} else {
			overallPassed++
		}
		out = append(out, map[string]any{
			"qname":         sym.QName,
			"name":          sym.Name,
			"file":          sym.File,
			"line":          sym.StartLine,
			"kind":          sym.Kind,
			"exists":        r.exists,
			"substantive":   r.substantive,
			"wired":         r.wired,
			"export_level":  r.exportLevel,
			"callers_count": r.callersCount,
			"notes":         r.notes,
		})
	}

	resp := map[string]any{
		"ok":           true,
		"count":        len(out),
		"passed":       overallPassed,
		"failed":       overallFailed,
		"results":      out,
		"hint":         "exists=symbol in index; substantive=body isn't a stub; wired=≥1 caller; export_level=caller in a different file. A failed level usually means the implementation is incomplete or the symbol isn't actually used.",
	}
	if note := freshnessNote(refreshed, oldHead, newHead); note != nil {
		resp["index_refreshed"] = note
	}
	return resp
}

type verifyResult struct {
	exists       bool
	substantive  bool
	wired        bool
	exportLevel  bool
	callersCount int
	notes        []string
}

func verifyOne(store *codeindex.Store, sym codeindex.Symbol, cwd string) verifyResult {
	r := verifyResult{}
	if sym.File == "" || sym.Name == "" {
		r.notes = append(r.notes, "symbol not found in index — re-run /index if the file was just written, or check the qname spelling")
		return r
	}
	r.exists = true

	// Substantive — read the symbol body from disk and check for stub
	// shapes. Robust to all 5 supported languages because the stub
	// patterns we screen for are language-agnostic ("TODO", "not
	// implemented", a single trivial statement).
	body, bodyErr := readSymbolBody(cwd, sym)
	if bodyErr != nil {
		r.notes = append(r.notes, "could not read body: "+bodyErr.Error())
	} else {
		r.substantive, r.notes = classifySubstantive(body, sym.Kind, r.notes)
	}

	// Wired — any caller at all.
	callers, _ := store.CallersOf(sym.QName, sym.Name)
	r.callersCount = len(callers)
	r.wired = r.callersCount > 0

	// Export-level — at least one caller is in a different file. Pull
	// each caller's file via the symbol it resolves to.
	if r.wired {
		seenFiles := map[string]bool{}
		for _, c := range callers {
			callerSym, _ := store.GetSymbol(c.CallerQName)
			if callerSym == nil {
				// Caller qname doesn't resolve to an indexed symbol —
				// could be a name-only edge from a regex extractor.
				// We can't tell where it lives; skip but don't mark
				// it as different-file by default.
				continue
			}
			if callerSym.File != sym.File {
				seenFiles[callerSym.File] = true
			}
		}
		r.exportLevel = len(seenFiles) > 0
		if r.wired && !r.exportLevel {
			r.notes = append(r.notes, "wired but every caller is in the same file — symbol isn't used externally yet")
		}
	} else if sym.Kind != "main" && sym.Kind != "init" {
		// Entry points (Go main, init) are never called; they're not
		// supposed to be wired. Don't flag them as a problem.
		r.notes = append(r.notes, "no callers — implementation may be unwired or only invoked dynamically (reflection, registry pattern, exported API). Decide whether that's expected.")
	}

	// Special case: entry points are inherently exported "not by being
	// called from elsewhere" — boost their export_level so the
	// 4-level check passes.
	if sym.Kind == "main" || sym.Kind == "init" {
		r.wired = true
		r.exportLevel = true
		if r.callersCount == 0 {
			r.notes = append(r.notes, "entry point — wired/export checks waived")
		}
	}

	return r
}

// readSymbolBody reads the lines [StartLine..EndLine] from the file
// declaring the symbol. Returns the joined text. Best-effort —
// caller treats read errors as "could not check substantiveness".
func readSymbolBody(cwd string, sym codeindex.Symbol) (string, error) {
	abs := filepath.Join(cwd, sym.File)
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var sb strings.Builder
	line := 0
	for sc.Scan() {
		line++
		if line < sym.StartLine {
			continue
		}
		if sym.EndLine > 0 && line > sym.EndLine {
			break
		}
		sb.Write(sc.Bytes())
		sb.WriteByte('\n')
	}
	return sb.String(), sc.Err()
}

// stubPatterns flag bodies that are obviously placeholder/stub:
// single-line panic("not implemented"), raise NotImplementedError,
// throw new Error("..."), pass-only Python defs, TODO-only comments.
var (
	reTODOOnly        = regexp.MustCompile(`(?i)\b(todo|fixme|xxx|hack)\b`)
	reNotImplemented  = regexp.MustCompile(`(?i)not\s*implemented|unimplemented|todo!\s*\(\)`)
	rePyPassOnly      = regexp.MustCompile(`(?m)^\s*pass\s*$`)
	rePanicOrThrow    = regexp.MustCompile(`(?i)\b(panic|raise|throw)\b`)
	reSingleStatement = regexp.MustCompile(`(?i)^\s*(return|pass|throw|raise|panic|todo)\b`)
)

// classifySubstantive applies the stub heuristics. Returns
// (substantive, notes-with-explanations). Conservative — when the
// shape is ambiguous, we prefer "true" with a note rather than
// false-flagging real one-liner functions.
func classifySubstantive(body, kind string, notes []string) (bool, []string) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false, append(notes, "body is empty")
	}

	// Strip the first line (the declaration itself) so we measure
	// the body proper.
	lines := strings.Split(trimmed, "\n")
	bodyLines := []string{}
	for i, l := range lines {
		if i == 0 {
			continue
		}
		s := strings.TrimSpace(l)
		if s == "" {
			continue
		}
		// Drop lone braces in C-family to avoid "} \n }" tripping the
		// not-much-here check.
		if s == "}" || s == "{" || s == "};" {
			continue
		}
		bodyLines = append(bodyLines, s)
	}

	// Empty body after declaration line — substantive only for kinds
	// where that's expected (interface, type alias).
	if len(bodyLines) == 0 {
		switch kind {
		case "interface", "type", "enum", "const", "var":
			return true, notes // declarations without a body are fine
		}
		return false, append(notes, "body is empty after the declaration line")
	}

	// Comment-only body — every line is a // / # / /* / * comment.
	// Catches TODO-only stubs regardless of how many comment lines
	// the agent wrote. Run this first so a single TODO comment
	// trips correctly.
	nonCommentLines := 0
	for _, l := range bodyLines {
		if strings.HasPrefix(l, "//") || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "/*") || strings.HasPrefix(l, "*") {
			continue
		}
		nonCommentLines++
	}
	if nonCommentLines == 0 {
		return false, append(notes, "body is comment-only (no executable code)")
	}

	// Single-statement bodies that match obvious stub shapes.
	if len(bodyLines) == 1 {
		first := bodyLines[0]
		if reSingleStatement.MatchString(first) && reNotImplemented.MatchString(first) {
			return false, append(notes, "single-statement stub: "+truncForNote(first, 80))
		}
		if rePyPassOnly.MatchString(first) {
			return false, append(notes, "Python `pass` stub")
		}
		// Single panic/throw with "not implemented" or TODO is a stub.
		if rePanicOrThrow.MatchString(first) && (reNotImplemented.MatchString(first) || reTODOOnly.MatchString(first)) {
			return false, append(notes, "single-line stub (raises/throws/panics with not-implemented marker)")
		}
		// Single return value (e.g. "return null;") is borderline —
		// could be a real one-liner. Flag with a note but pass.
		notes = append(notes, "body is a single statement — confirm it's the intended implementation, not a placeholder")
		return true, notes
	}

	return true, notes
}

func truncForNote(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
