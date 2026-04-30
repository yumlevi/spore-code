// Package sessionlog ports acorn/session_writer.py — per-session JSONL at
// ~/.spore-code/sessions/<safe-id>.jsonl. Matches the Python file format so a
// user can switch between the Python and Go CLIs and still see their
// history in /resume.
package sessionlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Writer is an append-only JSONL sink for a single session's history.
type Writer struct {
	path         string
	sessionID    string
	file         *os.File
	messageCount int
}

// Open creates/opens the session file. Writes the _meta header on new files.
func Open(globalDir, sessionID string) (*Writer, error) {
	dir := filepath.Join(globalDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, safeID(sessionID)+".jsonl")
	isNew := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		isNew = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{path: path, sessionID: sessionID, file: f}
	if isNew {
		w.append(map[string]any{"_meta": true, "session_id": sessionID, "created": float64(time.Now().Unix())})
	}
	return w, nil
}

// safeID matches Python's character substitution.
func safeID(id string) string {
	r := strings.NewReplacer(":", "_", "@", "_", "/", "_", "\\", "_")
	s := r.Replace(id)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func (w *Writer) append(record map[string]any) {
	if record == nil {
		return
	}
	if _, ok := record["ts"]; !ok {
		record["ts"] = float64(time.Now().UnixMilli()) / 1000.0
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return
	}
	_ = w.file.Sync()
	w.messageCount++
}

func (w *Writer) WriteUser(text string) {
	w.append(map[string]any{"role": "user", "text": text})
}

func (w *Writer) WriteAssistant(text string, usage map[string]any, iterations int) {
	r := map[string]any{"role": "assistant", "text": text}
	if usage != nil {
		r["usage"] = usage
	}
	if iterations > 0 {
		r["iterations"] = iterations
	}
	w.append(r)
}

func (w *Writer) WriteTool(name string, input any, result any, local bool, durationMs int) {
	ins, _ := json.Marshal(input)
	rs, _ := json.Marshal(result)
	w.append(map[string]any{
		"role":           "tool",
		"name":           name,
		"input":          truncate(string(ins), 500),
		"result_preview": truncate(string(rs), 500),
		"local":          local,
		"ms":             durationMs,
	})
}

func (w *Writer) WriteError(msg string) {
	w.append(map[string]any{"role": "error", "text": msg})
}

func (w *Writer) Path() string { return w.path }

func (w *Writer) Close() {
	_ = w.file.Close()
}

// Entry is a decoded JSONL line (excluding _meta).
type Entry struct {
	Role           string  `json:"role,omitempty"`
	Text           string  `json:"text,omitempty"`
	Name           string  `json:"name,omitempty"`
	Input          string  `json:"input,omitempty"`
	ResultPreview  string  `json:"result_preview,omitempty"`
	Local          bool    `json:"local,omitempty"`
	Ms             int     `json:"ms,omitempty"`
	Iterations     int     `json:"iterations,omitempty"`
	TS             float64 `json:"ts,omitempty"`
}

// LoadSession reads all non-meta lines from a session file.
func LoadSession(globalDir, sessionID string) []Entry {
	path := filepath.Join(globalDir, "sessions", safeID(sessionID)+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var peek struct {
			Meta bool `json:"_meta"`
		}
		if json.Unmarshal([]byte(line), &peek) == nil && peek.Meta {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// ProjectSession is a summary row for the /resume picker.
type ProjectSession struct {
	SessionID    string
	Path         string
	Modified     time.Time
	TimeAgo      string
	MessageCount int
	Preview      string
}

// ListProjectSessions returns sessions keyed by the same prefix Python builds.
func ListProjectSessions(globalDir, user, projectRoot string) []ProjectSession {
	dir := filepath.Join(globalDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	name := filepath.Base(projectRoot)
	h := sha256.Sum256([]byte(projectRoot))
	pathHash := hex.EncodeToString(h[:])[:8]
	prefix := fmt.Sprintf("cli_%s_%s-%s", user, name, pathHash)

	type withMtime struct {
		name string
		mod  time.Time
	}
	var sorted []withMtime
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sorted = append(sorted, withMtime{e.Name(), info.ModTime()})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].mod.After(sorted[j].mod) })

	var out []ProjectSession
	for _, item := range sorted {
		full := filepath.Join(dir, item.name)
		content, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var firstUser, lastAssistant, metaSession string
		msgCount := 0
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var peek struct {
				Meta     bool   `json:"_meta"`
				SessID   string `json:"session_id,omitempty"`
				Role     string `json:"role,omitempty"`
				Text     string `json:"text,omitempty"`
			}
			if json.Unmarshal([]byte(line), &peek) != nil {
				continue
			}
			if peek.Meta {
				if peek.SessID != "" {
					metaSession = peek.SessID
				}
				continue
			}
			msgCount++
			if peek.Role == "user" && firstUser == "" {
				firstUser = truncate(peek.Text, 100)
			}
			if peek.Role == "assistant" {
				lastAssistant = truncate(peek.Text, 100)
			}
		}
		if msgCount == 0 {
			continue
		}
		sid := metaSession
		if sid == "" {
			sid = strings.TrimSuffix(item.name, ".jsonl")
		}
		preview := firstUser
		if preview == "" {
			preview = lastAssistant
		}
		if preview == "" {
			preview = "(empty)"
		}
		out = append(out, ProjectSession{
			SessionID:    sid,
			Path:         full,
			Modified:     item.mod,
			TimeAgo:      humanAge(item.mod),
			MessageCount: msgCount,
			Preview:      preview,
		})
	}
	return out
}

// humanAge formats "5m ago", "3h ago", or "2026-04-21 15:00" like Python.
func humanAge(t time.Time) string {
	age := time.Since(t)
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return t.Format("2006-01-02 15:04")
	}
}

// CleanupOld removes session files older than keepDays.
func CleanupOld(globalDir string, keepDays int) int {
	dir := filepath.Join(globalDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	removed := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
