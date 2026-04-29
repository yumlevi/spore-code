package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNLines writes N synthetic lines ("line 1\n", "line 2\n", …)
// into a temp file and returns its absolute path.
func writeNLines(t *testing.T, n int) (path, cwd string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "test.txt")
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, dir
}

func TestReadFile_OffsetLimit(t *testing.T) {
	path, cwd := writeNLines(t, 100)
	r := ReadFile(map[string]any{"path": path, "offset": 10, "limit": 5}, cwd, "expanded")
	m, ok := r.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T: %+v", r, r)
	}
	if m["totalLines"].(int) != 100 {
		t.Errorf("totalLines: want 100, got %v", m["totalLines"])
	}
	got := m["content"].(string)
	// Lines should be 11..15 (offset 10 is 0-based = 11th line)
	want := "11\tline 11\n12\tline 12\n13\tline 13\n14\tline 14\n15\tline 15\n"
	if got != want {
		t.Errorf("content mismatch\nwant: %q\ngot:  %q", want, got)
	}
}

func TestReadFile_NegativeOffset_TailMode(t *testing.T) {
	path, cwd := writeNLines(t, 50)
	r := ReadFile(map[string]any{"path": path, "offset": -3}, cwd, "expanded")
	m := r.(map[string]any)
	if m["totalLines"].(int) != 50 {
		t.Errorf("totalLines: want 50, got %v", m["totalLines"])
	}
	if m["firstLine"].(int) != 48 {
		t.Errorf("firstLine: want 48, got %v", m["firstLine"])
	}
	got := m["content"].(string)
	want := "48\tline 48\n49\tline 49\n50\tline 50\n"
	if got != want {
		t.Errorf("content mismatch\nwant: %q\ngot:  %q", want, got)
	}
}

func TestReadFile_NegativeOffset_LargerThanFile(t *testing.T) {
	path, cwd := writeNLines(t, 5)
	// Asking for last 100 from a 5-line file should just return all 5.
	r := ReadFile(map[string]any{"path": path, "offset": -100}, cwd, "expanded")
	m := r.(map[string]any)
	if m["totalLines"].(int) != 5 {
		t.Errorf("totalLines: want 5, got %v", m["totalLines"])
	}
	got := m["content"].(string)
	if !strings.Contains(got, "1\tline 1") || !strings.Contains(got, "5\tline 5") {
		t.Errorf("expected all 5 lines, got: %q", got)
	}
}

func TestReadFile_OffsetBeyondFile(t *testing.T) {
	path, cwd := writeNLines(t, 10)
	r := ReadFile(map[string]any{"path": path, "offset": 1000, "limit": 5}, cwd, "expanded")
	m := r.(map[string]any)
	if m["totalLines"].(int) != 10 {
		t.Errorf("totalLines: want 10, got %v", m["totalLines"])
	}
	if m["content"].(string) != "" {
		t.Errorf("expected empty content past EOF, got: %q", m["content"])
	}
}

func TestReadFile_FileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	// Create a sparse file with size > MaxReadFileBytes via Truncate.
	// No actual disk usage, but Stat reports the cap-exceeding size.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(MaxReadFileBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()
	r := ReadFile(map[string]any{"path": path}, dir, "expanded")
	m, ok := r.(map[string]string)
	if !ok {
		t.Fatalf("expected error map, got %T: %+v", r, r)
	}
	if !strings.Contains(m["error"], "file too large") {
		t.Errorf("expected 'file too large' error, got: %v", m["error"])
	}
}

func TestReadFile_DefaultLimit(t *testing.T) {
	// 50 lines, no limit specified → default 2000 → return all.
	path, cwd := writeNLines(t, 50)
	r := ReadFile(map[string]any{"path": path}, cwd, "expanded")
	m := r.(map[string]any)
	if m["totalLines"].(int) != 50 {
		t.Errorf("totalLines: want 50, got %v", m["totalLines"])
	}
	got := m["content"].(string)
	if !strings.Contains(got, "1\tline 1") || !strings.Contains(got, "50\tline 50") {
		t.Errorf("expected all 50 lines, got first/last incomplete")
	}
}
