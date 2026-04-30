package app

import (
	"testing"
)

// TestScriptNamesInCommand — saved-helper-path detection in shell
// command lines. Tokens that match helperPathPatterns produce a
// derived script name; everything else is silently skipped.
func TestScriptNamesInCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		// Linux-style paths.
		{"python3 .spore-code/scratch/gen_qr.py", []string{"qr"}},
		{"./gen_qr.py", []string{"qr"}},
		{"bash .spore-code/scratch/lan_helper.sh", []string{"lan-helper"}}, // _helper suffix kept; only gen_/print_/dump_ prefixes are stripped
		// Windows-style paths get normalized via ReplaceAll.
		{`C:\Users\esfle\proj\.acorn\scratch\gen_qr.py`, []string{"qr"}},
		{`uv run --with qrcode python .acorn\scratch\gen_qr.py`, []string{"qr"}},
		// Multiple helpers in one command line.
		{`python3 .spore-code/scratch/gen_qr.py && bash dump_logs.sh`, []string{"qr", "logs"}}, // gen_ + dump_ prefixes stripped
		// Same helper twice → deduped.
		{`python3 .spore-code/scratch/gen_qr.py; python3 .spore-code/scratch/gen_qr.py`, []string{"qr"}},
		// Non-helper paths → no matches.
		{"npx expo start --lan", nil},
		{"netstat -ano | findstr :8081", nil},
		{"curl http://localhost:8081/status", nil},
		{`type C:\Users\esfle\proj\.acorn\logs\bg-3.log`, nil}, // .spore-code/logs ≠ .spore-code/scratch
		// Edge: empty / whitespace.
		{"", nil},
		{"   ", nil},
	}
	for _, tc := range cases {
		got := scriptNamesInCommand(tc.cmd)
		if !slicesEqual(got, tc.want) {
			t.Errorf("scriptNamesInCommand(%q): want %v, got %v", tc.cmd, tc.want, got)
		}
	}
}

// TestIsExecResultOK — exec result classification. The shape mirrors
// internal/tools.Exec output: {ok: bool, exit: int, stdout, stderr,
// timed_out, error}. Any non-zero exit, error, or timeout = failure.
func TestIsExecResultOK(t *testing.T) {
	cases := []struct {
		desc   string
		result any
		want   bool
	}{
		{"ok=true exit=0", map[string]any{"ok": true, "exit": 0}, true},
		{"ok=true exit=1", map[string]any{"ok": true, "exit": 1}, false},
		{"ok=false", map[string]any{"ok": false}, false},
		{"timed_out", map[string]any{"ok": true, "exit": 0, "timed_out": true}, false},
		{"error string", map[string]any{"error": "command not found"}, false},
		{"missing exit defaults to ok", map[string]any{"ok": true}, true},
		{"non-map result", "string-result", false},
		{"nil", nil, false},
		{"float exit code (JSON-decoded ints come through as float64)", map[string]any{"ok": true, "exit": float64(0)}, true},
		{"float exit non-zero", map[string]any{"ok": true, "exit": float64(2)}, false},
	}
	for _, tc := range cases {
		got := isExecResultOK(tc.result)
		if got != tc.want {
			t.Errorf("%s: want %v, got %v", tc.desc, tc.want, got)
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
