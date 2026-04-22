package app

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// historyPath returns the path used by both the Python and Go CLIs.
func historyPath(globalDir string) string {
	return filepath.Join(globalDir, "history")
}

// loadHistory reads ~/.acorn/history into a slice. Newest entries last.
// Lines beginning with `#` are skipped (matches some prompt_toolkit
// FileHistory variants); blank lines too.
func loadHistory(globalDir string) []string {
	f, err := os.Open(historyPath(globalDir))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		// prompt_toolkit FileHistory format prefixes with `+` for the actual
		// line; tolerate both.
		if strings.HasPrefix(line, "+") {
			line = line[1:]
		}
		out = append(out, line)
	}
	// Cap to last 1000 entries to keep cycling responsive.
	if len(out) > 1000 {
		out = out[len(out)-1000:]
	}
	return out
}

// appendHistory appends a single command to the history file. Idempotent
// dedupe — if it matches the last entry, skip.
func appendHistory(globalDir, line string) {
	line = strings.TrimRight(line, "\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	_ = os.MkdirAll(globalDir, 0o755)
	// Match the prompt_toolkit FileHistory format: a `# <iso ts>` line
	// followed by `+<text>`. Other acorn (and us on next load) tolerates
	// either format, but writing the prefixed form keeps the files
	// fully interchangeable.
	f, err := os.OpenFile(historyPath(globalDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n+" + line + "\n")
}
