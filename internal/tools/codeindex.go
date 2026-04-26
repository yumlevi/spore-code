package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// IndexProgress carries a periodic status update from IndexCodebase.
// Slash-command callers wire a sendProgramMsg-style callback so the
// TUI can show real progress instead of a single "indexing…" line
// that looks like a hang on big repos. Sent at most once per progress
// interval (default 2s) regardless of how fast files stream in.
type IndexProgress struct {
	FilesScanned int
	FilesParsed  int
	FilesSkipped int
	Symbols      int
	Calls        int
	ElapsedMs    int
	Note         string // free-form, e.g. "walking…", "parsing src/foo.ts"
}

// IndexProgressFn is the callback signature. Nil = no progress events.
type IndexProgressFn func(IndexProgress)

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
	return IndexCodebaseWithProgress(input, cwd, nil)
}

// IndexCodebaseWithProgress is the slash-command-friendly variant that
// accepts a progress callback. The agent-callable IndexCodebase wrapper
// passes nil so its result envelope stays JSON-clean.
func IndexCodebaseWithProgress(input map[string]any, cwd string, onProgress IndexProgressFn) any {
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
	// Counters are read by the heartbeat goroutine while the walker
	// writes them, so they need to be word-atomic. atomic.Int64 keeps
	// the reads tear-free without taking a mutex on every file.
	var (
		totalFiles   atomic.Int64
		totalSymbols atomic.Int64
		totalCalls   atomic.Int64
		totalImports atomic.Int64
		skipped      atomic.Int64
		scanned      atomic.Int64
		unsupported  atomic.Int64
	)
	// currentPath holds the file the walker is presently inside (if
	// any). Set on callback entry, cleared on exit. Surfaced in the
	// heartbeat note so a stuck file is visible to the user — e.g.
	// "processing src/foo.ts" sitting for 30s tells you exactly which
	// file the parser hung on.
	var currentPath atomic.Pointer[string]
	unsupportedByLang := map[string]int{}
	walkErr := error(nil)

	// emitProgress is invoked by both the heartbeat goroutine and the
	// final/synthesis emits. Always sends — the throttling lives in
	// the ticker rather than this helper now, so the explicit
	// "walking…"/"committing…" markers fire reliably.
	emitProgress := func(note string) {
		if onProgress == nil {
			return
		}
		onProgress(IndexProgress{
			FilesScanned: int(scanned.Load()),
			FilesParsed:  int(totalFiles.Load()),
			FilesSkipped: int(skipped.Load()),
			Symbols:      int(totalSymbols.Load()),
			Calls:        int(totalCalls.Load()),
			ElapsedMs:    int(time.Since(start).Milliseconds()),
			Note:         note,
		})
	}

	emitProgress("walking…")

	// Heartbeat goroutine — fires every 2s regardless of whether the
	// walker has yielded a file. Surfaces the in-flight file path
	// when the walker is inside the per-file callback, so a hung
	// extractor or read is identifiable rather than just appearing
	// stuck.
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ticker.C:
					note := "walking…"
					if cp := currentPath.Load(); cp != nil && *cp != "" {
						note = "processing " + *cp
					}
					emitProgress(note)
				}
			}
		}()
	}

	for _, r := range roots {
		err := codeindex.Walk(codeindex.WalkOptions{
			Root:      r,
			Languages: langFilter,
			MaxFiles:  maxFiles,
		}, func(fe codeindex.FileEntry) bool {
			// Surface the in-flight file path so the heartbeat can
			// show "processing <path>" — diagnostic when a single
			// file hangs the extractor.
			p := fe.RelPath
			currentPath.Store(&p)
			defer func() {
				var empty *string
				currentPath.Store(empty)
			}()
			scanned.Add(1)
			// Skip unchanged files when not forcing — mtime check.
			if !force {
				if prev, ok, _ := store.FileMTime(fe.RelPath); ok && prev == fe.MTime {
					skipped.Add(1)
					return true
				}
			}
			// Wrap ExtractFile in a 30s timeout. os.ReadFile follows
			// symlinks and has no native cancellation; a stalled
			// network-mount target or a kernel bug could otherwise
			// hang us forever. The goroutine is "abandoned" on
			// timeout (no shared state — ExtractFile only reads the
			// file and returns a value), so worst case we leak one
			// goroutine per stuck file.
			res := extractWithTimeout(fe, 30*time.Second)
			if res.Err != nil && len(res.Symbols) == 0 {
				// Distinguish "no extractor for this language" (py/rs
				// today) from "parser failed on this file". The former
				// is a coverage gap to surface to the user; we still
				// upsert the file row so /architecture and the
				// by-language breakdown reflect the project's true
				// composition. The latter is logged but not counted.
				if strings.Contains(res.Err.Error(), "no extractor") {
					unsupported.Add(1)
					unsupportedByLang[fe.Language]++
					_ = tx.DeleteFile(fe.RelPath)
					if err := tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", 0); err != nil {
						return false
					}
				}
				return true
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
				totalSymbols.Add(1)
			}
			for _, c := range res.Calls {
				if err := tx.AddCall(codeindex.Call{
					CallerQName: c.CallerQName,
					CalleeQName: c.CalleeQName,
					Line:        c.Line,
				}, c.CalleeName, fe.RelPath); err != nil {
					return false
				}
				totalCalls.Add(1)
			}
			for _, im := range res.Imports {
				if err := tx.AddImport(fe.RelPath, im.Target, im.Alias, im.Line); err != nil {
					return false
				}
				totalImports.Add(1)
			}
			totalFiles.Add(1)
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
	emitProgress("committing…")
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
		"ok":                  true,
		"files":               int(totalFiles.Load()),
		"skipped":             int(skipped.Load()),
		"unsupported":         int(unsupported.Load()),
		"unsupported_by_lang": unsupportedByLang,
		"symbols":             int(totalSymbols.Load()),
		"calls":               int(totalCalls.Load()),
		"imports":             int(totalImports.Load()),
		"took_ms":             int(time.Since(start).Milliseconds()),
		"index_head":          stats.IndexHead,
		"total_files":         stats.Files,
		"total_syms":          stats.Symbols,
		"by_language":         stats.ByLanguage,
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

// MaxTraceCallsEdges caps the total edges returned by trace_calls so a
// hot symbol can't blow the agent's context budget. 200 keeps the
// response compact (≈ 5 KB JSON) while leaving headroom for paths
// through medium-depth fan-out.
const MaxTraceCallsEdges = 200

// TraceCalls walks CALLS edges from a starting symbol, in callers and/or
// callees direction, up to depth N. Returns flat edges; the agent
// reconstructs paths or asks again with a tighter depth.
//
// Input fields:
//   name:       string — matches callee_name (works regardless of qname resolution)
//   qname:      string — exact callee/caller match (preferred when known)
//   direction:  callers | callees | both (default: callers)
//   depth:      1..5 (default: 3)
//   limit:      total edge cap; default and max 200
func TraceCalls(input map[string]any, cwd string) any {
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	name := asString(input["name"], "")
	qname := asString(input["qname"], "")
	dir := strings.ToLower(asString(input["direction"], "callers"))
	depth := asInt(input["depth"], 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	limit := asInt(input["limit"], MaxTraceCallsEdges)
	if limit <= 0 || limit > MaxTraceCallsEdges {
		limit = MaxTraceCallsEdges
	}
	if name == "" && qname == "" {
		return errMap("name or qname is required")
	}

	type edge struct {
		Caller string `json:"caller"`
		Callee string `json:"callee"`
		Name   string `json:"callee_name"`
		Line   int    `json:"line"`
		Depth  int    `json:"depth"`
	}
	var edges []edge
	visited := map[string]bool{}
	truncated := false

	walkCallers := func(seedQName, seedName string) {
		// BFS frontier of (qname, name) — we may have only one or the other.
		type frontierItem struct {
			qname, name string
			d           int
		}
		queue := []frontierItem{{seedQName, seedName, 0}}
		key := seedQName
		if key == "" {
			key = seedName
		}
		visited[key] = true
		for len(queue) > 0 && !truncated {
			cur := queue[0]
			queue = queue[1:]
			if cur.d >= depth {
				continue
			}
			callers, err := store.CallersOf(cur.qname, cur.name)
			if err != nil {
				continue
			}
			for _, c := range callers {
				if len(edges) >= limit {
					truncated = true
					break
				}
				edges = append(edges, edge{
					Caller: c.CallerQName,
					Callee: c.CalleeQName,
					Name:   cur.name,
					Line:   c.Line,
					Depth:  cur.d + 1,
				})
				if !visited[c.CallerQName] {
					visited[c.CallerQName] = true
					sym, _ := store.GetSymbol(c.CallerQName)
					nextName := ""
					if sym != nil {
						nextName = sym.Name
					}
					queue = append(queue, frontierItem{c.CallerQName, nextName, cur.d + 1})
				}
			}
		}
	}

	walkCallees := func(seedQName, seedName string) {
		// For callees we need a starting qname — the caller_qname column is
		// always populated, so we can BFS down via CalleesOf.
		if seedQName == "" {
			// Try to resolve a qname from the seedName via Search.
			res, _ := store.Search(codeindex.SearchQuery{NameLike: seedName, Limit: 5})
			for _, r := range res {
				if r.Name == seedName {
					seedQName = r.QName
					break
				}
			}
		}
		if seedQName == "" {
			return
		}
		type frontierItem struct {
			qname string
			d     int
		}
		queue := []frontierItem{{seedQName, 0}}
		visited[seedQName] = true
		for len(queue) > 0 && !truncated {
			cur := queue[0]
			queue = queue[1:]
			if cur.d >= depth {
				continue
			}
			callees, err := store.CalleesOf(cur.qname)
			if err != nil {
				continue
			}
			for _, c := range callees {
				if len(edges) >= limit {
					truncated = true
					break
				}
				edges = append(edges, edge{
					Caller: c.CallerQName,
					Callee: c.CalleeQName,
					Line:   c.Line,
					Depth:  cur.d + 1,
				})
				next := c.CalleeQName
				if next != "" && !visited[next] {
					visited[next] = true
					queue = append(queue, frontierItem{next, cur.d + 1})
				}
			}
		}
	}

	switch dir {
	case "callers":
		walkCallers(qname, name)
	case "callees":
		walkCallees(qname, name)
	case "both":
		walkCallers(qname, name)
		walkCallees(qname, name)
	default:
		return errMap("direction must be one of: callers, callees, both")
	}

	return map[string]any{
		"ok":         true,
		"root_name":  name,
		"root_qname": qname,
		"direction":  dir,
		"depth":      depth,
		"edges":      edges,
		"truncated":  truncated,
		"count":      len(edges),
	}
}

// Impact maps a list of paths (or the current git diff) to affected
// symbols and counts their transitive callers up to depth=2 — answering
// "what could a change in this file break?" in token-cheap form.
//
// Input fields:
//   paths:     []string — repo-relative; default: `git diff --name-only HEAD`
//   depth:     1..3 (default: 2) — caller transitive depth
//   limit:     total symbol cap (default 100)
func Impact(input map[string]any, cwd string) any {
	store, err := codeindex.Open(cwd)
	if err != nil {
		return errMap("open index: " + err.Error())
	}
	defer store.Close()

	paths := asStringSlice(input["paths"])
	if len(paths) == 0 {
		paths = gitChangedPaths(cwd)
	}
	if len(paths) == 0 {
		return map[string]any{"ok": true, "paths": nil, "affected_symbols": []any{}, "note": "no paths supplied and `git diff --name-only HEAD` is empty"}
	}

	depth := asInt(input["depth"], 2)
	if depth < 1 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	limit := asInt(input["limit"], 100)
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	// Collect all symbols defined in the changed files. Each entry is a
	// map for JSON-friendly typing — same shape as SearchSymbols
	// results so the agent gets a consistent envelope across tools.
	type affected struct {
		qname, name, file, kind string
		line                    int
		transitiveCallers       int
	}
	var symbols []affected

	for _, p := range paths {
		res, err := store.Search(codeindex.SearchQuery{FileLike: p, Limit: 500})
		if err != nil {
			continue
		}
		for _, r := range res {
			symbols = append(symbols, affected{
				qname: r.QName, name: r.Name, file: r.File, line: r.StartLine, kind: r.Kind,
			})
			if len(symbols) >= limit {
				break
			}
		}
		if len(symbols) >= limit {
			break
		}
	}

	// Compute transitive caller count via BFS up to depth.
	for i := range symbols {
		symbols[i].transitiveCallers = countTransitiveCallers(store, symbols[i].qname, symbols[i].name, depth)
	}

	// Sort hottest first.
	for i := 1; i < len(symbols); i++ {
		for j := i; j > 0 && symbols[j-1].transitiveCallers < symbols[j].transitiveCallers; j-- {
			symbols[j-1], symbols[j] = symbols[j], symbols[j-1]
		}
	}

	out := make([]map[string]any, 0, len(symbols))
	totalCallers := 0
	for _, s := range symbols {
		out = append(out, map[string]any{
			"qname":              s.qname,
			"name":               s.name,
			"file":               s.file,
			"line":               s.line,
			"kind":               s.kind,
			"transitive_callers": s.transitiveCallers,
		})
		totalCallers += s.transitiveCallers
	}

	return map[string]any{
		"ok":               true,
		"paths":            paths,
		"depth":            depth,
		"affected_symbols": out,
		"total_callers":    totalCallers,
	}
}

// countTransitiveCallers BFS-counts unique callers up to depth.
func countTransitiveCallers(s *codeindex.Store, qname, name string, depth int) int {
	type item struct {
		qname, name string
		d           int
	}
	visited := map[string]bool{}
	if qname != "" {
		visited[qname] = true
	} else {
		visited[name] = true
	}
	queue := []item{{qname, name, 0}}
	count := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.d >= depth {
			continue
		}
		callers, err := s.CallersOf(cur.qname, cur.name)
		if err != nil {
			continue
		}
		for _, c := range callers {
			if visited[c.CallerQName] {
				continue
			}
			visited[c.CallerQName] = true
			count++
			sym, _ := s.GetSymbol(c.CallerQName)
			nextName := ""
			if sym != nil {
				nextName = sym.Name
			}
			queue = append(queue, item{c.CallerQName, nextName, cur.d + 1})
		}
	}
	return count
}

// extractWithTimeout runs ExtractFile in a goroutine and gives up
// after `d`. On timeout we return a synthetic Err result so the caller
// records the file as a parse failure (and surfaces it in logs) rather
// than blocking the whole indexer. The original goroutine continues
// until os.ReadFile returns — that's a goroutine leak, but it doesn't
// touch the index transaction (ExtractFile has no access to tx) so
// it's safe to abandon.
func extractWithTimeout(fe codeindex.FileEntry, d time.Duration) codeindex.ExtractResult {
	resCh := make(chan codeindex.ExtractResult, 1)
	go func() {
		resCh <- codeindex.ExtractFile(fe)
	}()
	select {
	case r := <-resCh:
		return r
	case <-time.After(d):
		return codeindex.ExtractResult{
			Err: fmt.Errorf("codeindex: extract timed out after %s on %s", d, fe.RelPath),
		}
	}
}

// gitChangedPaths returns the set of paths in `git diff --name-only HEAD`
// limited to in-repo POSIX paths. Empty slice when git unavailable or no
// diff. Used as the default `paths` for Impact.
func gitChangedPaths(cwd string) []string {
	cmd := exec.Command("git", "-C", cwd, "diff", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		paths = append(paths, line)
	}
	return paths
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
		// Default: all 5 known extensions. Languages without an
		// extractor yet (py/rs) still get walked so the user sees
		// them in the by-language breakdown — IndexCodebase records
		// them as `unsupported` and surfaces a clear note rather than
		// silently producing 0 files.
		return map[string]bool{
			codeindex.LangGo:     true,
			codeindex.LangTS:     true,
			codeindex.LangJS:     true,
			codeindex.LangPython: true,
			codeindex.LangRust:   true,
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
