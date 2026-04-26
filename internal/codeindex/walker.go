package codeindex

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxFileBytes caps the size of a single file the indexer will read.
// Files above this are skipped (often generated bundles or vendored
// blobs that bury the genuine architecture).
const MaxFileBytes = 2 * 1024 * 1024 // 2 MiB

// noiseDirs are skipped during the walk. Mirrors the list the agent
// prompt advertises in plugins/acorn-cli/index.js buildProjectContextSection
// (line ~389) so the index reflects the same project view the user sees.
var noiseDirs = map[string]struct{}{
	".git":             {},
	"node_modules":     {},
	".venv":            {},
	"venv":             {},
	"env":              {}, // virtualenv default name
	"__pycache__":      {},
	"dist":             {},
	"build":            {},
	"target":           {},
	"out":              {}, // common JS/Java output dir
	".next":            {},
	".cache":           {},
	".acorn":           {},
	"vendor":           {},
	".gradle":          {},
	".mvn":             {},
	".pytest_cache":    {},
	".mypy_cache":      {},
	".ruff_cache":      {},
	".turbo":           {},
	".nuxt":            {},
	".svelte-kit":      {},
	".terraform":       {},
	".idea":            {},
	".vscode":          {},
	"coverage":         {},
	".nyc_output":      {},
	".DS_Store":        {},
	"Pods":             {}, // CocoaPods / iOS
	"bower_components": {}, // legacy JS
	".pnpm-store":      {}, // pnpm
	".yarn":            {}, // yarn berry cache
	".parcel-cache":    {},
	".tox":             {}, // python tox
	".eggs":            {},
	".gem":             {}, // ruby
	"_build":           {}, // erlang/elixir
	"deps":             {}, // erlang/elixir
}

// Language constants keyed by detected extension. M1 supports go + ts/js;
// py + rs land in M2 alongside CALLS extraction.
const (
	LangGo     = "go"
	LangTS     = "ts"
	LangJS     = "js"
	LangPython = "py"
	LangRust   = "rs"
)

// extToLanguage maps file extension (lowercase, with dot) to language
// constant. Unknown extensions yield "" and the walker skips the file.
var extToLanguage = map[string]string{
	".go":   LangGo,
	".ts":   LangTS,
	".tsx":  LangTS,
	".mts":  LangTS,
	".cts":  LangTS,
	".js":   LangJS,
	".jsx":  LangJS,
	".mjs":  LangJS,
	".cjs":  LangJS,
	".py":   LangPython,
	".rs":   LangRust,
}

// FileEntry is one source file found by the walker.
type FileEntry struct {
	AbsPath  string // absolute path on disk
	RelPath  string // repo-relative posix path (forward slashes)
	Language string
	Size     int64
	MTime    int64 // unix seconds
}

// WalkOptions controls walker filtering.
type WalkOptions struct {
	Root             string
	Languages        map[string]bool // if non-nil, only emit files whose language is true here
	ExtraIgnoreFiles []string        // additional files (glob-free, relative paths) to skip
	MaxFiles         int             // 0 → unlimited (caller-side cap)
}

// Walk emits every source file under Root that survives the noise-dir
// filter, the language filter, and the size cap. The yield callback
// returns false to stop iteration early.
func Walk(opts WalkOptions, yield func(FileEntry) bool) error {
	if opts.Root == "" {
		return errors.New("codeindex: walk root required")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return fmt.Errorf("codeindex: abs root: %w", err)
	}

	ignored := loadAcornIgnore(root)
	for _, p := range opts.ExtraIgnoreFiles {
		ignored[filepath.ToSlash(p)] = struct{}{}
	}

	count := 0
	walkErr := filepath.WalkDir(root, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied or vanished file — skip the entry, keep walking.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if absPath == root {
				return nil
			}
			if _, hit := noiseDirs[name]; hit {
				return fs.SkipDir
			}
			// Skip *.egg-info dirs without enumerating every variant.
			if strings.HasSuffix(name, ".egg-info") {
				return fs.SkipDir
			}
			// Hidden dirs default to skipped except for .github (CI lives
			// there and isn't usually source we care about either).
			if strings.HasPrefix(name, ".") && name != "." && name != ".." {
				return fs.SkipDir
			}
			return nil
		}
		// File entries.
		ext := strings.ToLower(filepath.Ext(name))
		lang, ok := extToLanguage[ext]
		if !ok {
			return nil
		}
		if opts.Languages != nil && !opts.Languages[lang] {
			return nil
		}
		rel, relErr := filepath.Rel(root, absPath)
		if relErr != nil {
			return nil
		}
		relPosix := filepath.ToSlash(rel)
		if _, hit := ignored[relPosix]; hit {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		// Skip non-regular files: FIFOs, sockets, char/block devices,
		// and symlinks. os.ReadFile follows symlinks and can block
		// indefinitely on a special file or a stale network-mount
		// target. The walker is "what's part of the project source",
		// not "what's reachable through links" — keep it tight.
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > MaxFileBytes {
			return nil
		}
		entry := FileEntry{
			AbsPath:  absPath,
			RelPath:  relPosix,
			Language: lang,
			Size:     info.Size(),
			MTime:    info.ModTime().Unix(),
		}
		if !yield(entry) {
			return fs.SkipAll
		}
		count++
		if opts.MaxFiles > 0 && count >= opts.MaxFiles {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return walkErr
	}
	return nil
}

// loadAcornIgnore reads <root>/.acornignore (one path per line, # for
// comments, blank lines ignored) into a set of repo-relative posix paths.
// Patterns are exact path matches — this is intentionally simple; if
// pattern globbing is wanted, .gitignore handles most of the cases via
// the noise-dir filter and explicit project conventions.
func loadAcornIgnore(root string) map[string]struct{} {
	out := map[string]struct{}{}
	f, err := os.Open(filepath.Join(root, ".acornignore"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[filepath.ToSlash(line)] = struct{}{}
	}
	return out
}

// HashContent returns a short hex sha1 of the file's contents. Used to
// detect actual changes when mtime alone is unreliable (git checkout,
// rsync). 7 hex chars matches git's short-sha convention.
func HashContent(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if len(sum) > 7 {
		return sum[:7], nil
	}
	return sum, nil
}
