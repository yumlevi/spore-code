package tools

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yumlevi/acorn-cli/internal/codeindex"
)

// codeindex tool surface (M1):
//   index_codebase   — walk cwd, parse, populate .acorn/index.db
//   search_symbols   — name/kind/file/language filters against the index
//   get_snippet      — return source lines for a symbol (qname or file:line range)
//   architecture     — clusters/entry-points/hot-paths/tech-stack summary
//
// Each handler opens the store fresh per call. The DB is small and
// modernc.org/sqlite is fast enough that the simplicity beats a cached
// handle for v1; if benchmarks show real overhead we can lift a cached
// store onto the Executor later.

// IndexCodebase walks cwd, extracts symbols, writes them to the index.
// Idempotent: every file's prior symbols/calls/imports are deleted
// before re-insert so a re-run produces a clean state.
//
// Input fields:
//   roots: []string         — optional sub-directories to constrain the walk; defaults to cwd
//   languages: []string     — filter (e.g. ["go", "ts"]); default is all supported
//   max_files: int          — safety cap; 0 = unlimited
//   force: bool             — re-index everything even if mtime unchanged
func IndexCodebase(input map[string]any, cwd string) any {
	if cwd == "" {
		return errMap("cwd is empty; cannot index")
	}
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	langs := asStringSlice(input["languages"])
	maxFiles := asInt(input["max_files"], 0)
	force := asBool(input["force"], false)

	langFilter := buildLanguageFilter(langs)

	tx, err := store.BeginIndex()
	if err != nil {
		return errMap("begin index tx: " + err.Error())
	}

	roots := asStringSlice(input["roots"])
	if len(roots) == 0 {
		roots = []string{cwd}
	} else {
		// Sandbox: every root must be inside cwd.
		safe := make([]string, 0, len(roots))
		for _, r := range roots {
			abs, err := filepath.Abs(r)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(cwd, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue // skip out-of-cwd roots
			}
			safe = append(safe, abs)
		}
		if len(safe) == 0 {
			tx.Rollback()
			return errMap("no roots survived sandbox check; pass paths under cwd")
		}
		roots = safe
	}

	start := time.Now()
	totalFiles := 0
	totalSymbols := 0
	totalCalls := 0
	totalImports := 0
	skipped := 0
	walkErr := error(nil)

	for _, r := range roots {
		err := codeindex.Walk(codeindex.WalkOptions{
			Root:      r,
			Languages: langFilter,
			MaxFiles:  maxFiles,
		}, func(fe codeindex.FileEntry) bool {
			// Skip unchanged files when not forcing — mtime check.
			if !force {
				if prev, ok, _ := store.FileMTime(fe.RelPath); ok && prev == fe.MTime {
					skipped++
					return true
				}
			}
			res := codeindex.ExtractFile(fe)
			if res.Err != nil && len(res.Symbols) == 0 {
				return true // best-effort; skip the file but keep walking
			}
			// Refresh: drop prior rows for this file before re-insert.
			if err := tx.DeleteFile(fe.RelPath); err != nil {
				return false
			}
			if err := tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", len(res.Symbols)); err != nil {
				return false
			}
			for _, s := range res.Symbols {
				if err := tx.UpsertSymbol(s); err != nil {
					return false
				}
				totalSymbols++
			}
			for _, c := range res.Calls {
				if err := tx.AddCall(codeindex.Call{
					CallerQName: c.CallerQName,
					CalleeQName: c.CalleeQName,
					Line:        c.Line,
				}, c.CalleeName, fe.RelPath); err != nil {
					return false
				}
				totalCalls++
			}
			for _, im := range res.Imports {
				if err := tx.AddImport(fe.RelPath, im.Target, im.Alias, im.Line); err != nil {
					return false
				}
				totalImports++
			}
			totalFiles++
			return true
		})
		if err != nil {
			walkErr = err
			break
		}
	}
	if walkErr != nil {
		tx.Rollback()
		return errMap("walk: " + walkErr.Error())
	}
	if err := tx.Commit(); err != nil {
		return errMap("commit: " + err.Error())
	}

	// Record current git head if we can — drives the "is the index
	// stale?" decision for next session bootstrap.
	if sha := readGitHead(cwd); sha != "" {
		_ = store.SetIndexHead(sha)
	}

	stats, _ := store.Stats()
	return map[string]any{
		"ok":          true,
		"files":       totalFiles,
		"skipped":     skipped,
		"symbols":     totalSymbols,
		"calls":       totalCalls,
		"imports":     totalImports,
		"took_ms":     int(time.Since(start).Milliseconds()),
		"index_head":  stats.IndexHead,
		"total_files": stats.Files,
		"total_syms":  stats.Symbols,
		"by_language": stats.ByLanguage,
	}
}

// SearchSymbols runs a query against the index. Cheap; meant to replace
// most grep+read_file pairs in plan-mode Phase 2.
//
// Input fields:
//   name:        string   — case-insensitive substring match against symbol.name
//   qname:       string   — substring match against symbol.qname
//   kind:        string   — exact match (function, method, class, struct, interface, type, const, var, enum, constructor)
//   file:        string   — LIKE pattern over file path
//   language:    string   — go | ts | js | py | rs
//   exported:    bool     — restrict to exported symbols
//   limit:       int      — default 200, hard cap returned
func SearchSymbols(input map[string]any, cwd string) any {
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	q := codeindex.SearchQuery{
		NameLike:   asString(input["name"], ""),
		Kind:       asString(input["kind"], ""),
		FileLike:   asString(input["file"], ""),
		Language:   asString(input["language"], ""),
		OnlyExport: asBool(input["exported"], false),
		Limit:      asInt(input["limit"], 0),
	}

	results, err := store.Search(q)
	if err != nil {
		return errMap("search: " + err.Error())
	}

	// Optional qname substring filter — applied post-query so the SQL
	// stays simple. Cheap because Search already capped the result.
	if qpat := asString(input["qname"], ""); qpat != "" {
		filtered := results[:0]
		for _, r := range results {
			if strings.Contains(strings.ToLower(r.QName), strings.ToLower(qpat)) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		out = append(out, map[string]any{
			"name":      r.Name,
			"qname":     r.QName,
			"kind":      r.Kind,
			"file":      r.File,
			"line":      r.StartLine,
			"end_line":  r.EndLine,
			"signature": r.Signature,
			"container": r.Container,
			"language":  r.Language,
			"exported":  r.Exported,
		})
	}
	return map[string]any{"ok": true, "count": len(out), "results": out}
}

// GetSnippet returns the source body for a symbol or a file:line range.
//
// Input fields (one form):
//   qname:       string — symbol's qualified name (preferred)
//
// Input fields (alternate form):
//   file:        string — repo-relative path
//   start_line:  int
//   end_line:    int
func GetSnippet(input map[string]any, cwd string) any {
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	var file string
	var startLine, endLine int

	if qn := asString(input["qname"], ""); qn != "" {
		sym, err := store.GetSymbol(qn)
		if err != nil {
			return errMap("lookup qname: " + err.Error())
		}
		if sym == nil {
			return map[string]any{"ok": false, "error": "qname not in index", "qname": qn}
		}
		file = sym.File
		startLine = sym.StartLine
		endLine = sym.EndLine
	} else {
		file = asString(input["file"], "")
		startLine = asInt(input["start_line"], 0)
		endLine = asInt(input["end_line"], 0)
		if file == "" || startLine < 1 {
			return errMap("either qname or (file + start_line[+end_line]) required")
		}
	}

	abs := filepath.Join(cwd, file)
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errMap("file outside cwd: " + file)
	}

	body, err := readLines(abs, startLine, endLine)
	if err != nil {
		return errMap("read snippet: " + err.Error())
	}
	return map[string]any{
		"ok":         true,
		"file":       file,
		"start_line": startLine,
		"end_line":   endLine,
		"content":    body,
	}
}

// Architecture returns the index summary used by /architecture and the
// agent's pre-plan orientation.
func Architecture(input map[string]any, cwd string) any {
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	arch, err := codeindex.ComputeArchitecture(store)
	if err != nil {
		return errMap("compute architecture: " + err.Error())
	}
	return map[string]any{
		"ok":           true,
		"index_head":   arch.IndexHead,
		"tech_stack":   arch.TechStack,
		"entry_points": arch.EntryPoints,
		"clusters":     arch.Clusters,
		"hot_paths":    arch.HotPaths,
		"stats":        arch.Stats,
		"notes":        arch.Notes,
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func errMap(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }

// asInt / asBool / asString come from fileops.go in this package.

func asStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}

func buildLanguageFilter(langs []string) map[string]bool {
	if len(langs) == 0 {
		return map[string]bool{
			codeindex.LangGo: true,
			codeindex.LangTS: true,
			codeindex.LangJS: true,
			// py + rs walked but not yet extracted — gate them out at the
			// walker so we don't generate empty file rows.
		}
	}
	out := make(map[string]bool, len(langs))
	for _, l := range langs {
		out[strings.ToLower(strings.TrimSpace(l))] = true
	}
	return out
}

// readLines returns lines [start..end] (1-indexed, inclusive) from path.
// end<=0 means "to EOF". start<=0 maps to 1.
func readLines(path string, start, end int) (string, error) {
	if start < 1 {
		start = 1
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024) // tolerate long lines
	var sb strings.Builder
	line := 0
	for sc.Scan() {
		line++
		if line < start {
			continue
		}
		if end > 0 && line > end {
			break
		}
		sb.Write(sc.Bytes())
		sb.WriteByte('\n')
	}
	return sb.String(), sc.Err()
}

// readGitHead returns the abbreviated git sha at HEAD or "" if unavailable.
func readGitHead(cwd string) string {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--short=12", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
