package tools

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yumlevi/acorn-cli/internal/bg"
)

// Server-side tools: we must NOT claim these; signal fallback by returning
// nil from Execute and the caller sends no tool:result (server executes).
var serverTools = map[string]bool{
	"graph_query": true, "graph_update": true, "graph_delete": true, "query_about": true,
	"message_send": true, "message_react": true, "message_edit": true, "message_read": true,
	"delegate_task": true, "task_status": true, "task_cancel": true, "task_update": true,
	"save_tool": true, "skill_lookup": true, "skill_update": true,
	"session_status": true, "sessions_list": true, "env_manage": true,
	"notify_user": true, "web_search": true,
	"anima_list": true, "anima_message": true, "anima_graph": true, "anima_manage": true,
	"browser": true, "startup_tasks": true, "data_poller": true,
	"remote_exec": true, "remote_read_file": true, "remote_write_file": true, "ssh_tunnel": true,
	"list_custom_tools": true,
	// New SPORE server-side additions we don't claim:
	"schedule_wakeup": true, "list_wakeups": true, "cancel_wakeup": true,
	"task_create": true, "task_progress": true, "task_list": true, "task_get": true,
	"log_watch": true, "log_watch_list": true, "log_watch_stop": true,
	"ask_user": true,
}

// Local tools we implement.
var localTools = map[string]bool{
	"exec":       true,
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"glob":       true,
	"grep":       true,
	// web_serve / web_fetch left to server for now (mirrors Python's web_fetch).

	// codeindex (M1): structural code search backed by a per-project
	// SQLite index at <cwd>/.acorn/index.db. See internal/codeindex/.
	"index_codebase": true,
	"search_symbols": true,
	"get_snippet":    true,
	"architecture":   true,

	// codeindex (M2): call graph + change-impact analysis.
	"trace_calls": true,
	"impact":      true,
}

// DelegationMode controls what the agent may delegate via delegate_task.
type DelegationMode string

const (
	DelegateDefault  DelegationMode = "default"
	DelegateOff      DelegationMode = "off"
	DelegateResearch DelegationMode = "research"
	DelegateCode     DelegationMode = "code"
	DelegateAll      DelegationMode = "all"
)

// Permissions is the minimal interface the executor needs. TuiPermissions
// implementation lives in the app package where it can open modals.
type Permissions interface {
	// IsAutoApproved returns true if the tool+input combo is pre-approved
	// (e.g. read-only by nature, or covered by a user-added allow rule).
	IsAutoApproved(tool string, input map[string]any) bool
	// Prompt blocks until the user allows / denies. Return true = allowed.
	Prompt(tool string, input map[string]any) bool
}

// Hooks the UI can register for side-effects on specific tool runs.
type Hooks struct {
	// OnExecLine fires for each stdout line from an `exec` call. Handy for
	// live streaming of subprocess output into a status bar.
	OnExecLine func(line string)
	// OnCodeView fires after a successful read_file; UI can show the
	// content in a side panel.
	OnCodeView func(path, content string, isNew bool)
	// OnCodeDiff fires after a successful edit_file.
	OnCodeDiff func(path, oldText, newText string)
	// OnToolDone fires after every claimed tool call with its final
	// result, so the caller can persist to the session writer.
	OnToolDone func(name string, input map[string]any, result any, durationMs int)
}

type Executor struct {
	Perms      Permissions
	CWD        string
	LogDir     string
	Delegation DelegationMode
	// Scope mirrors Model.scope — "" / "strict" enforces the cwd
	// sandbox in fileops; "expanded" lifts it. Set by app/update.go
	// whenever /scope changes.
	Scope      string
	Hooks      Hooks
	BG         *bg.Manager

	// Track the one currently-running child for /stop handling. Go's
	// context.CancelFunc replaces the Python _current_proc.kill() pattern.
	mu     sync.Mutex
	abort  func()
}

func New(perms Permissions, cwd, logDir string) *Executor {
	return &Executor{
		Perms:      perms,
		CWD:        cwd,
		LogDir:     logDir,
		Delegation: DelegateDefault,
		BG:         bg.New(logDir),
	}
}

// AbortCurrent cancels the in-flight tool call (if any) — called on /stop.
func (e *Executor) AbortCurrent() {
	e.mu.Lock()
	ab := e.abort
	e.mu.Unlock()
	if ab != nil {
		ab()
	}
}

// Execute dispatches a tool:request. Returns:
//   - (result, true) — we handled it, caller sends tool:result with `result`.
//   - (nil, false)   — server-side tool; caller sends nothing, server handles.
func (e *Executor) Execute(name string, inputRaw json.RawMessage) (result any, claimed bool) {
	var input map[string]any
	_ = json.Unmarshal(inputRaw, &input)
	if input == nil {
		input = map[string]any{}
	}

	// Delegation policing — intercept before server sees it.
	if name == "delegate_task" {
		if err := e.checkDelegation(input); err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		return nil, false
	}

	if serverTools[name] || !localTools[name] {
		return nil, false
	}

	if !e.Perms.IsAutoApproved(name, input) {
		if !e.Perms.Prompt(name, input) {
			r := map[string]string{"error": "Denied by user"}
			if e.Hooks.OnToolDone != nil {
				e.Hooks.OnToolDone(name, input, r, 0)
			}
			return r, true
		}
	}

	start := time.Now()
	defer func() {
		if claimed && e.Hooks.OnToolDone != nil {
			e.Hooks.OnToolDone(name, input, result, int(time.Since(start).Milliseconds()))
		}
	}()

	switch name {
	case "read_file":
		r := ReadFile(input, e.CWD, e.Scope)
		if e.Hooks.OnCodeView != nil {
			if m, ok := r.(map[string]any); ok {
				if content, ok := m["content"].(string); ok {
					e.Hooks.OnCodeView(asString(input["path"], ""), content, false)
				}
			}
		}
		return r, true
	case "write_file":
		content := asString(input["content"], "")
		r := WriteFile(input, e.CWD, e.Scope)
		if e.Hooks.OnCodeView != nil {
			if m, ok := r.(map[string]any); ok && m["ok"] == true {
				e.Hooks.OnCodeView(asString(input["path"], ""), content, true)
			}
		}
		return r, true
	case "edit_file":
		r := EditFile(input, e.CWD, e.Scope)
		if e.Hooks.OnCodeDiff != nil {
			if m, ok := r.(map[string]any); ok && m["ok"] == true {
				e.Hooks.OnCodeDiff(
					asString(input["path"], ""),
					asString(input["old_string"], asString(input["old_text"], "")),
					asString(input["new_string"], asString(input["new_text"], "")),
				)
			}
		}
		return r, true
	case "glob":
		return Glob(input, e.CWD), true
	case "grep":
		return Grep(input, e.CWD), true
	case "exec":
		return Exec(input, e.CWD, e.LogDir, e.BG, e.Hooks.OnExecLine), true

	// codeindex (M1)
	case "index_codebase":
		return IndexCodebase(input, e.CWD), true
	case "search_symbols":
		return SearchSymbols(input, e.CWD), true
	case "get_snippet":
		return GetSnippet(input, e.CWD), true
	case "architecture":
		return Architecture(input, e.CWD), true

	// codeindex (M2)
	case "trace_calls":
		return TraceCalls(input, e.CWD), true
	case "impact":
		return Impact(input, e.CWD), true
	}
	return nil, false
}

func (e *Executor) checkDelegation(input map[string]any) error {
	mode := e.Delegation
	task := asString(input["task"], "") + " " + asString(input["context"], "")
	lower := toLower(task)
	switch mode {
	case DelegateOff:
		return fmt.Errorf("Delegation is disabled. Do this task yourself inline using the available tools.")
	case DelegateResearch:
		for _, w := range []string{"write", "create file", "edit file", "generate code", "build", "implement", "scaffold"} {
			if containsWord(lower, w) {
				return fmt.Errorf("Delegation mode is 'research' — you can only delegate web research, not file writes.")
			}
		}
	case DelegateDefault:
		for _, w := range []string{"build the", "implement the", "create the project", "scaffold", "set up the"} {
			if containsWord(lower, w) {
				return fmt.Errorf("Delegation mode is 'default' — do not delegate main task orchestration.")
			}
		}
	}
	return nil
}

func toLower(s string) string {
	// Avoid importing strings just for ToLower in a hot path? Small helper.
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func containsWord(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	if len(n) == 0 {
		return 0
	}
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
