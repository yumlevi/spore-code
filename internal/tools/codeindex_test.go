package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// allowAllPerms auto-approves every tool. The codeindex tools are
// read/index-only so this is the test equivalent of /mode auto.
type allowAllPerms struct{}

func (allowAllPerms) IsAutoApproved(string, map[string]any) bool { return true }
func (allowAllPerms) Prompt(string, map[string]any) bool         { return true }

// findRepoRoot walks up looking for go.mod; same trick the codeindex
// tests use.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		root = filepath.Dir(root)
	}
	t.Skip("repo root with go.mod not found")
	return ""
}

// TestExecutorCodeIndexFlow drives index_codebase + search_symbols +
// get_snippet + architecture through Executor.Execute as if the agent
// had requested them over WS. Validates wiring + dispatch + helper
// reuse from fileops.go.
func TestExecutorCodeIndexFlow(t *testing.T) {
	root := findRepoRoot(t)
	tmp := t.TempDir()

	// The executor reads CWD for sandboxing. We point it at the tmp
	// dir but feed index_codebase a `roots` arg pointing at the real
	// repo so it can index something. The roots-sandbox check requires
	// roots to live under cwd, so instead use the repo root as cwd.
	logDir := filepath.Join(tmp, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	exe := New(allowAllPerms{}, root, logDir)

	// Clean any prior index from the repo's .acorn dir so the test
	// starts fresh.
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	})

	// 1. index_codebase
	in1, _ := json.Marshal(map[string]any{"languages": []string{"go"}, "force": true})
	r1, claimed := exe.Execute("index_codebase", in1)
	if !claimed {
		t.Fatal("index_codebase should be claimed")
	}
	m1, ok := r1.(map[string]any)
	if !ok || m1["ok"] != true {
		t.Fatalf("index_codebase failed: %+v", r1)
	}
	if files, _ := m1["files"].(int); files == 0 {
		t.Errorf("expected files > 0, got %v", m1["files"])
	}

	// 2. search_symbols for Execute (method)
	in2, _ := json.Marshal(map[string]any{"name": "Execute", "kind": "method"})
	r2, _ := exe.Execute("search_symbols", in2)
	m2, _ := r2.(map[string]any)
	if m2["ok"] != true {
		t.Fatalf("search_symbols failed: %+v", r2)
	}
	results, _ := m2["results"].([]map[string]any)
	if len(results) == 0 {
		t.Fatalf("search_symbols returned 0 results")
	}
	foundExec := false
	var execQName string
	for _, r := range results {
		if r["name"] == "Execute" && r["container"] == "Executor" {
			foundExec = true
			execQName, _ = r["qname"].(string)
			break
		}
	}
	if !foundExec {
		t.Fatalf("expected Executor.Execute in results; got %d", len(results))
	}

	// 3. get_snippet by qname
	in3, _ := json.Marshal(map[string]any{"qname": execQName})
	r3, _ := exe.Execute("get_snippet", in3)
	m3, _ := r3.(map[string]any)
	if m3["ok"] != true {
		t.Fatalf("get_snippet failed: %+v", r3)
	}
	content, _ := m3["content"].(string)
	if !strings.Contains(content, "func (e *Executor) Execute") {
		t.Errorf("snippet should include func decl line; got first 80 chars: %q", firstChars(content, 80))
	}

	// 4. architecture
	r4, _ := exe.Execute("architecture", []byte(`{}`))
	m4, _ := r4.(map[string]any)
	if m4["ok"] != true {
		t.Fatalf("architecture failed: %+v", r4)
	}
	if m4["clusters"] == nil || m4["tech_stack"] == nil {
		t.Errorf("architecture missing fields: %v", m4)
	}
	t.Logf("architecture: stats=%+v notes=%v", m4["stats"], m4["notes"])
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestIndexNonForceDoesNotDeadlock indexes a project twice without
// force=true. The second run hits the FileMTime mtime-skip path while
// an index transaction is open. With MaxOpenConns=1 this used to
// deadlock forever (BeginIndex held the only conn, FileMTime needed
// another). Regression test for the bug that bit users running a
// fresh /index in the wild.
func TestIndexNonForceDoesNotDeadlock(t *testing.T) {
	root := findRepoRoot(t)
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	exe := New(allowAllPerms{}, root, logDir)

	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	})

	// First run: force=true so files get inserted with current mtime.
	in1, _ := json.Marshal(map[string]any{"languages": []string{"go"}, "force": true})
	r1, _ := exe.Execute("index_codebase", in1)
	if m1, ok := r1.(map[string]any); !ok || m1["ok"] != true {
		t.Fatalf("first /index failed: %+v", r1)
	}

	// Second run: NO force. This is the exact path that deadlocked —
	// FileMTime gets called for every file while the tx is open.
	// Wrap in a goroutine + 30s timeout so a real deadlock fails the
	// test instead of hanging it.
	in2, _ := json.Marshal(map[string]any{"languages": []string{"go"}})
	done := make(chan any, 1)
	go func() {
		r, _ := exe.Execute("index_codebase", in2)
		done <- r
	}()
	select {
	case r := <-done:
		m, ok := r.(map[string]any)
		if !ok || m["ok"] != true {
			t.Fatalf("second /index failed: %+v", r)
		}
		// Skipped count should equal the number of files indexed
		// the first time (every file was unchanged).
		if skipped, _ := m["skipped"].(int); skipped == 0 {
			t.Errorf("expected non-zero skipped on re-index, got %v", m["skipped"])
		}
		t.Logf("re-index: files=%v skipped=%v took_ms=%v", m["files"], m["skipped"], m["took_ms"])
	case <-time.After(30 * time.Second):
		t.Fatal("non-force re-index deadlocked (>30s)")
	}
}

// TestExecutorTraceCallsAndImpact exercises the M2 tools through
// Executor.Execute. Indexes acorn-cli itself, then asks "who calls
// Open?" and "what does a change to internal/codeindex/store.go
// affect?" — both should return non-empty results.
func TestExecutorTraceCallsAndImpact(t *testing.T) {
	root := findRepoRoot(t)
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	exe := New(allowAllPerms{}, root, logDir)

	// Reset prior index so the test is self-contained.
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
	_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-shm"))
		_ = os.Remove(filepath.Join(root, ".spore-code", "index.db-wal"))
	})

	in1, _ := json.Marshal(map[string]any{"languages": []string{"go"}, "force": true})
	r1, _ := exe.Execute("index_codebase", in1)
	if m, _ := r1.(map[string]any); m["ok"] != true {
		t.Fatalf("index_codebase failed: %+v", r1)
	}

	// trace_calls direction=callers name=Open
	in2, _ := json.Marshal(map[string]any{"name": "Open", "direction": "callers", "depth": 2})
	r2, _ := exe.Execute("trace_calls", in2)
	m2, _ := r2.(map[string]any)
	if m2["ok"] != true {
		t.Fatalf("trace_calls failed: %+v", r2)
	}
	count, _ := m2["count"].(int)
	if count == 0 {
		t.Fatalf("trace_calls returned 0 callers for Open; expected at least one (e.g. our own tools/codeindex.go callers)")
	}
	t.Logf("trace_calls Open callers: count=%d truncated=%v", count, m2["truncated"])

	// impact paths=[internal/codeindex/store.go]
	in3, _ := json.Marshal(map[string]any{"paths": []string{"internal/codeindex/store.go"}, "depth": 2})
	r3, _ := exe.Execute("impact", in3)
	m3, _ := r3.(map[string]any)
	if m3["ok"] != true {
		t.Fatalf("impact failed: %+v", r3)
	}
	syms, _ := m3["affected_symbols"].([]map[string]any)
	if len(syms) == 0 {
		t.Fatalf("impact returned 0 affected symbols for store.go; expected the public API")
	}
	t.Logf("impact: %d symbols affected, total_callers=%v", len(syms), m3["total_callers"])
	for i, s := range syms {
		if i >= 5 {
			break
		}
		t.Logf("  - %s @ %s:%v  tc=%v", s["name"], s["file"], s["line"], s["transitive_callers"])
	}
}
