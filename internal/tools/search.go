package tools

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// globMatch supports doublestar (`**` spans `/`) and forward-slash paths
// on every OS, which Go's stdlib filepath.Match does NOT (single `*`
// doesn't cross `/`, and on Windows the path separator is `\` so a
// pattern containing `/` would never match a real path).
//
// Translation rules:
//   `**`  → `.*`           (zero or more chars, including `/`)
//   `*`   → `[^/]*`         (zero or more non-`/` chars — single segment)
//   `?`   → `[^/]`           (single non-`/` char)
//   else  → regexp.QuoteMeta-ed literal
//
// Cached per pattern (we hit the same patterns many times during a
// WalkDir loop). The candidate path is forward-slash-normalized
// before matching so the same pattern works on Windows.
var globReCache sync.Map // map[string]*regexp.Regexp

func globMatch(pattern, candidate string) bool {
	candidate = filepath.ToSlash(candidate)
	if v, ok := globReCache.Load(pattern); ok {
		return v.(*regexp.Regexp).MatchString(candidate)
	}
	var b strings.Builder
	b.WriteString(`\A`)
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(`.*`)
				i++ // consume the second *
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	globReCache.Store(pattern, re)
	return re.MatchString(candidate)
}

var noiseDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	"dist": true, "build": true, ".next": true, ".cache": true, "target": true,
}

// Glob implements the glob tool. Input: pattern, path.
func Glob(input map[string]any, cwd string) any {
	pattern := asString(input["pattern"], "*")
	searchPath := asString(input["path"], cwd)
	if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(cwd, searchPath)
	}

	var matches []string
	err := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || noiseDirs[name] {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(searchPath, path)
		// Three match attempts:
		//   1. full rel-path against the pattern  → "src/x/y.ts"
		//   2. just the basename against the pattern → "y.ts"
		//   3. wildcard-prefixed pattern against rel → handles agents
		//      that send "*.ts" expecting recursive match (which Go's
		//      stdlib filepath.Match does NOT do)
		// All three normalize to forward-slash via globMatch.
		if globMatch(pattern, rel) || globMatch(pattern, name) || globMatch("**/"+pattern, rel) {
			matches = append(matches, filepath.ToSlash(rel))
		}
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	if len(matches) > 500 {
		matches = matches[:500]
	}
	return map[string]any{"matches": matches, "count": len(matches)}
}

// Grep implements the grep tool. Input: pattern, path, glob/type, -i.
func Grep(input map[string]any, cwd string) any {
	pattern := asString(input["pattern"], "")
	if pattern == "" {
		return map[string]string{"error": "pattern is required"}
	}
	searchPath := asString(input["path"], cwd)
	if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(cwd, searchPath)
	}
	fileGlob := asString(input["glob"], asString(input["type"], ""))

	pre := ""
	if asBool(input["-i"], false) {
		pre = "(?i)"
	}
	re, err := regexp.Compile(pre + pattern)
	if err != nil {
		return map[string]string{"error": "Invalid regex: " + err.Error()}
	}

	type hit struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var results []hit
	truncated := false

	err = filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || noiseDirs[name] {
				return fs.SkipDir
			}
			return nil
		}
		if fileGlob != "" {
			// Same doublestar / cross-OS-friendly matcher as Glob.
			// Match against basename + rel-path so "*.go" works at
			// any depth and "src/**/*.ts" anchors from the root.
			rel, _ := filepath.Rel(searchPath, path)
			if !globMatch(fileGlob, d.Name()) && !globMatch(fileGlob, rel) && !globMatch("**/"+fileGlob, rel) {
				return nil
			}
		}
		rel, _ := filepath.Rel(searchPath, path)
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200]
				}
				results = append(results, hit{File: rel, Line: lineNo, Text: line})
				if len(results) >= 200 {
					truncated = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && !truncated {
		return map[string]string{"error": err.Error()}
	}
	out := map[string]any{"results": results, "count": len(results)}
	if truncated {
		out["truncated"] = true
	}
	return out
}
