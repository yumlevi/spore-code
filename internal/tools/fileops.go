// Package tools implements local tool execution — read_file, write_file,
// edit_file, glob, grep, exec. Port of acorn/tools/*.
//
// All tools accept JSON input (raw decoded into a map) and return a
// result value that the caller can marshal back into a tool:result frame.
package tools

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yumlevi/spore-code/internal/codeindex"
)

// ResolvePath enforces the cwd sandbox — tools may only touch paths inside
// the current working directory. Matches acorn/tools/file_ops.py:_resolve.
func ResolvePath(raw, cwd string) (string, error) {
	return ResolvePathScoped(raw, cwd, "")
}

// ResolvePathScoped is the scope-aware variant. scope == "expanded"
// skips the cwd containment check entirely — the user explicitly opted
// into broader access via /scope. Empty scope or "strict" enforces the
// containment as before.
func ResolvePathScoped(raw, cwd, scope string) (string, error) {
	if raw == "" {
		return "", errors.New("path is required")
	}
	var resolved string
	if filepath.IsAbs(raw) {
		resolved = filepath.Clean(raw)
	} else {
		resolved = filepath.Clean(filepath.Join(cwd, raw))
	}
	if scope == "expanded" {
		return resolved, nil
	}
	absCwd := filepath.Clean(cwd)
	if resolved != absCwd && !strings.HasPrefix(resolved, absCwd+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %s is outside the working directory %s (use /scope expanded to broaden)", resolved, cwd)
	}
	return resolved, nil
}

// MaxReadFileBytes caps the on-disk size we'll attempt to scan. Files
// larger than this return an error pointing at offset+limit or `exec
// head/tail` for unbounded log digging. Without a cap a 5 GB log
// would be quietly read end-to-end just to compute totalLines.
const MaxReadFileBytes int64 = 100 * 1024 * 1024 // 100 MB

// ReadFile implements the read_file tool.
//
// Range inputs:
//
//	offset + limit                — 0-based line offset and count
//	start_line + end_line         — 1-based inclusive line range
//	startLine + endLine           — camelCase aliases
//	line_range / lineRange        — "120-145", "120:145", or [120,145]
//
// Formatting inputs:
//
//	include_line_numbers=false    — omit "N\t" prefixes from content
//	compact=true / code_only=true — aliases for include_line_numbers=false
//
// scope governs sandboxing — empty/"strict" enforces cwd containment,
// "expanded" allows any absolute path.
//
// Memory profile: O(limit) string allocations regardless of file size.
// A 5 GB log with `offset: 5000000, limit: 50` allocates 50 strings,
// not 5 million — earlier implementations buffered every line of the
// file before slicing, which OOMed on big logs. The full-file scan
// still happens (we count `totalLines` so the agent knows whether to
// fetch more), but unselected lines are dropped after counting.
//
// Special offset values:
//
//	offset >= 0      — start at line `offset` (0-based)
//	offset < 0       — "last |offset| lines" (tail mode, ring-buffered)
func ReadFile(input map[string]any, cwd, scope string) any {
	pathRaw, _ := input["path"].(string)
	p, err := ResolvePathScoped(pathRaw, cwd, scope)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	info, err := os.Stat(p)
	if err != nil {
		return map[string]string{"error": "File not found: " + p}
	}
	if info.IsDir() {
		return map[string]string{"error": p + " is a directory"}
	}
	if info.Size() > MaxReadFileBytes {
		return map[string]string{
			"error": fmt.Sprintf("file too large: %d bytes > cap %d (%d MB). For huge logs, use `exec head -N` / `exec tail -N` / `exec sed -n 'a,bp'`, or grep_files for pattern-based extraction.",
				info.Size(), MaxReadFileBytes, MaxReadFileBytes/(1024*1024)),
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	defer f.Close()

	offset, limit, includeLineNumbers := readFileOptions(input)
	if limit <= 0 {
		limit = 2000
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	// tail mode: offset < 0 means "the last N lines". We don't know
	// totalLines up front (we'd need a full scan to count), so use a
	// ring buffer sized to the requested tail. O(N) memory where N is
	// the tail size, not the file size.
	if offset < 0 {
		tailN := -offset
		if tailN > limit {
			limit = tailN
		}
		ring := make([]string, 0, tailN)
		total := 0
		for sc.Scan() {
			total++
			if len(ring) < tailN {
				ring = append(ring, sc.Text())
			} else {
				// Shift left, append new (cheap for small N).
				copy(ring, ring[1:])
				ring[tailN-1] = sc.Text()
			}
		}
		startLine := total - len(ring) + 1
		var b strings.Builder
		for i, ln := range ring {
			writeReadFileLine(&b, startLine+i, ln, includeLineNumbers)
		}
		return map[string]any{
			"content":    b.String(),
			"totalLines": total,
			"firstLine":  startLine,
		}
	}

	// Forward mode: only collect lines in [offset, offset+limit).
	// Continue scanning after we have enough lines so totalLines is
	// accurate — useful for the agent deciding whether to call again.
	end := offset + limit
	var collected []string
	if limit > 0 {
		collected = make([]string, 0, limit)
	}
	total := 0
	for sc.Scan() {
		if total >= offset && total < end {
			collected = append(collected, sc.Text())
		}
		total++
	}
	if offset > total {
		offset = total
	}
	var b strings.Builder
	for i, ln := range collected {
		writeReadFileLine(&b, offset+i+1, ln, includeLineNumbers)
	}
	return map[string]any{"content": b.String(), "totalLines": total}
}

func readFileOptions(input map[string]any) (offset, limit int, includeLineNumbers bool) {
	offset = asInt(input["offset"], 0)
	limit = asInt(input["limit"], 2000)
	includeLineNumbers = true
	if asBool(input["compact"], false) || asBool(input["code_only"], false) || asBool(input["codeOnly"], false) {
		includeLineNumbers = false
	}
	if v, ok := input["include_line_numbers"]; ok {
		includeLineNumbers = asBool(v, includeLineNumbers)
	}
	if v, ok := input["includeLineNumbers"]; ok {
		includeLineNumbers = asBool(v, includeLineNumbers)
	}
	if v, ok := input["line_numbers"]; ok {
		includeLineNumbers = asBool(v, includeLineNumbers)
	}

	if start, end, ok := lineRangeFromInput(input); ok {
		if start < 1 {
			start = 1
		}
		offset = start - 1
		if end >= start {
			limit = end - start + 1
		}
	}
	return offset, limit, includeLineNumbers
}

func lineRangeFromInput(input map[string]any) (start, end int, ok bool) {
	for _, key := range []string{"line_range", "lineRange", "range"} {
		if s, e, found := parseLineRange(input[key]); found {
			return s, e, true
		}
	}
	start, hasStart := firstInt(input, "start_line", "startLine", "line")
	end, hasEnd := firstInt(input, "end_line", "endLine")
	if hasStart || hasEnd {
		if !hasStart {
			start = end
		}
		if !hasEnd {
			end = 0
		}
		return start, end, true
	}
	return 0, 0, false
}

func parseLineRange(v any) (start, end int, ok bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		if s == "" {
			return 0, 0, false
		}
		replacer := strings.NewReplacer("lines", "", "line", "", "l", "", " ", "", "..", "-", ":", "-", ",", "-")
		s = replacer.Replace(s)
		parts := strings.Split(s, "-")
		if len(parts) == 0 || parts[0] == "" {
			return 0, 0, false
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, false
		}
		end := 0
		if len(parts) > 1 && parts[1] != "" {
			if end, err = strconv.Atoi(parts[1]); err != nil {
				return 0, 0, false
			}
		}
		return start, end, true
	case []any:
		if len(x) == 0 {
			return 0, 0, false
		}
		start := asInt(x[0], 0)
		end := 0
		if len(x) > 1 {
			end = asInt(x[1], 0)
		}
		return start, end, start > 0
	case []int:
		if len(x) == 0 {
			return 0, 0, false
		}
		end := 0
		if len(x) > 1 {
			end = x[1]
		}
		return x[0], end, x[0] > 0
	}
	return 0, 0, false
}

func firstInt(input map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		if v, ok := input[key]; ok {
			return asInt(v, 0), true
		}
	}
	return 0, false
}

func writeReadFileLine(b *strings.Builder, lineNo int, line string, includeLineNumbers bool) {
	if includeLineNumbers {
		fmt.Fprintf(b, "%d\t%s\n", lineNo, line)
		return
	}
	b.WriteString(line)
	b.WriteByte('\n')
}

// WriteFile implements the write_file tool. Input: path, content.
func WriteFile(input map[string]any, cwd, scope string) any {
	pathRaw, _ := input["path"].(string)
	content, _ := input["content"].(string)
	p, err := ResolvePathScoped(pathRaw, cwd, scope)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return map[string]string{"error": err.Error()}
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return map[string]string{"error": err.Error()}
	}
	markCodeIndexDirty(cwd, p)
	return map[string]any{"ok": true, "path": p, "lines": strings.Count(content, "\n") + 1}
}

// EditFile implements the edit_file tool. Input: path, old_string (or
// old_text), new_string (or new_text), replace_all?.
func EditFile(input map[string]any, cwd, scope string) any {
	pathRaw, _ := input["path"].(string)
	p, err := ResolvePathScoped(pathRaw, cwd, scope)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	old := asString(input["old_string"], asString(input["old_text"], ""))
	replacement := asString(input["new_string"], asString(input["new_text"], ""))
	replaceAll := asBool(input["replace_all"], false)

	text := string(data)
	count := strings.Count(text, old)
	if count == 0 {
		return map[string]string{"error": "old_string not found in " + p}
	}
	if count > 1 && !replaceAll {
		return map[string]string{"error": fmt.Sprintf("old_string found %d times — not unique. Provide more context or use replace_all.", count)}
	}
	var updated string
	var reps int
	if replaceAll {
		updated = strings.ReplaceAll(text, old, replacement)
		reps = count
	} else {
		updated = strings.Replace(text, old, replacement, 1)
		reps = 1
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return map[string]string{"error": err.Error()}
	}
	markCodeIndexDirty(cwd, p)
	return map[string]any{"ok": true, "path": p, "replacements": reps}
}

// helpers
func asInt(v any, d int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return d
}
func asString(v any, d string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return d
}
func firstString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(asString(input[key], "")); s != "" {
			return s
		}
	}
	return ""
}
func asBool(v any, d bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b
		}
	}
	return d
}

// markCodeIndexDirty flags the freshly-written file in .spore-code/index.db
// so the next /index call re-parses it. Best-effort: silently no-ops
// when the index doesn't exist or the path is outside cwd. Never
// blocks the calling write — every file op that might've broken the
// index now signals the indexer rather than letting the index drift
// silently.
func markCodeIndexDirty(cwd, absPath string) {
	if cwd == "" || absPath == "" {
		return
	}
	rel, err := filepath.Rel(cwd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return // outside cwd → not in our index
	}
	relPosix := filepath.ToSlash(rel)
	// Cheap existence check — if .spore-code/index.db isn't there, we
	// haven't bootstrapped the index yet and there's nothing to dirty.
	if _, err := os.Stat(filepath.Join(cwd, ".spore-code", "index.db")); err != nil {
		return
	}
	store, err := codeindex.Open(cwd)
	if err != nil {
		return
	}
	defer store.Close()
	_ = store.MarkDirty(relPosix)
}
