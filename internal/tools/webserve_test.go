package tools

import (
	"strings"
	"testing"
)

// TestWebServeIsDisabled — web_serve is intentionally disabled for
// acorn sessions. Every action returns a clear error pointing the
// agent at exec. This test guards the behavior so the operator's
// "do not call web_serve at all" rule can't regress silently.
func TestWebServeIsDisabled(t *testing.T) {
	for _, action := range []string{"start", "stop", "status", "list", "backend", "anything-else"} {
		r := WebServe(map[string]any{"action": action, "dir": "/tmp"}, "/tmp", "expanded")
		m, ok := r.(map[string]any)
		if !ok {
			t.Errorf("action=%q: expected map result, got %T", action, r)
			continue
		}
		if m["ok"] != false {
			t.Errorf("action=%q: expected ok=false, got %+v", action, m)
		}
		errStr, _ := m["error"].(string)
		if !strings.Contains(errStr, "exec") {
			t.Errorf("action=%q: error should redirect to exec, got %q", action, errStr)
		}
		if m["hint"] != "use_exec_instead" {
			t.Errorf("action=%q: expected hint=use_exec_instead, got %v", action, m["hint"])
		}
	}
}
