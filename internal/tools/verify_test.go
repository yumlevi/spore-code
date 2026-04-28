package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyImplementation runs the 4-level check against acorn-cli's
// own indexed source. Uses the same allowAllPerms test executor as
// the other codeindex flows so the dispatch path through Execute is
// covered too.
func TestVerifyImplementation(t *testing.T) {
	root := findRepoRoot(t)
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	exe := New(allowAllPerms{}, root, logDir)

	// Reset prior index so the test runs deterministically.
	_ = os.Remove(filepath.Join(root, ".acorn", "index.db"))
	_ = os.Remove(filepath.Join(root, ".acorn", "index.db-shm"))
	_ = os.Remove(filepath.Join(root, ".acorn", "index.db-wal"))
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(root, ".acorn", "index.db"))
		_ = os.Remove(filepath.Join(root, ".acorn", "index.db-shm"))
		_ = os.Remove(filepath.Join(root, ".acorn", "index.db-wal"))
	})

	// Build a fresh Go-only index.
	in1, _ := json.Marshal(map[string]any{"languages": []string{"go"}, "force": true})
	r1, _ := exe.Execute("index_codebase", in1)
	if m, _ := r1.(map[string]any); m["ok"] != true {
		t.Fatalf("index_codebase failed: %+v", r1)
	}

	// Verify a known wired symbol — codeindex.Open. It exists, has a
	// real body, has callers (this very tool, the executor flow,
	// tests, etc.), and the callers live in different files.
	in2, _ := json.Marshal(map[string]any{"qnames": []string{"internal/codeindex/store.go::Open"}})
	r2, _ := exe.Execute("verify_implementation", in2)
	m2, _ := r2.(map[string]any)
	if m2["ok"] != true {
		t.Fatalf("verify_implementation failed: %+v", r2)
	}
	results, _ := m2["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d (%+v)", len(results), m2)
	}
	rOpen := results[0]
	t.Logf("Open result: %+v", rOpen)
	for _, level := range []string{"exists", "substantive", "wired", "export_level"} {
		if rOpen[level] != true {
			t.Errorf("Open should pass %q, got %+v", level, rOpen[level])
		}
	}
	if cc, _ := rOpen["callers_count"].(int); cc < 1 {
		t.Errorf("Open should have callers; got %v", rOpen["callers_count"])
	}

	// Verify a non-existent symbol — should report exists=false.
	in3, _ := json.Marshal(map[string]any{"qnames": []string{"internal/nope/nope.go::DoesNotExist"}})
	r3, _ := exe.Execute("verify_implementation", in3)
	m3, _ := r3.(map[string]any)
	results3, _ := m3["results"].([]map[string]any)
	if len(results3) != 1 || results3[0]["exists"] != false {
		t.Errorf("missing symbol should report exists=false; got %+v", m3)
	}

	// Path-based verification — ask for every symbol in store.go.
	in4, _ := json.Marshal(map[string]any{"paths": []string{"internal/codeindex/store.go"}})
	r4, _ := exe.Execute("verify_implementation", in4)
	m4, _ := r4.(map[string]any)
	count, _ := m4["count"].(int)
	if count < 5 {
		t.Errorf("path-based verify should report multiple symbols in store.go; got %d", count)
	}
	t.Logf("path verify: count=%d passed=%v failed=%v", count, m4["passed"], m4["failed"])

	// Total counts add up.
	if p, _ := m4["passed"].(int); p+(m4["failed"].(int)) != count {
		t.Errorf("passed + failed should equal count: %v + %v != %v", m4["passed"], m4["failed"], count)
	}
}

// TestClassifySubstantive — direct unit tests of the stub heuristics
// since they're language-agnostic and easy to enumerate.
func TestClassifySubstantive(t *testing.T) {
	cases := []struct {
		desc string
		body string
		kind string
		want bool
	}{
		{"empty body", "func F() {\n}\n", "function", false},
		{"empty interface — fine", "type Foo interface {\n}\n", "interface", true},
		{"Python pass-only stub", "def foo():\n    pass\n", "function", false},
		{"Python NotImplementedError", "def foo():\n    raise NotImplementedError()\n", "function", false},
		{"Go panic('not implemented')", "func F() {\n\tpanic(\"not implemented\")\n}\n", "function", false},
		{"JS throw not implemented", "function f() {\n  throw new Error('not implemented')\n}\n", "function", false},
		{"single-line return — borderline, allowed", "func F() int {\n\treturn 42\n}\n", "function", true},
		{"real multi-line implementation", "func F() int {\n\tx := 1\n\ty := 2\n\treturn x + y\n}\n", "function", true},
		{"comment-only body with TODO", "function f() {\n  // TODO: implement\n}\n", "function", false},
		{"single throw without not-implemented marker", "function f() {\n  throw new Error('user not found');\n}\n", "function", true},
	}
	for _, tc := range cases {
		got, notes := classifySubstantive(tc.body, tc.kind, nil)
		if got != tc.want {
			t.Errorf("%s: want substantive=%v, got %v (notes: %v)", tc.desc, tc.want, got, notes)
		}
	}
}
