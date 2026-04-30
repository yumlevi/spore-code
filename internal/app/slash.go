package app

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// slashSuggest holds the autocomplete state for the `/` command dropdown.
type slashSuggest struct {
	visible bool
	matches []slashEntry
	cursor  int
}

type slashEntry struct {
	cmd  string
	desc string
}

// All known commands + one-liner descriptions. Each command that takes
// discrete subcommands (mode/theme/scope/panel/etc.) gets a parent
// entry AND one entry per subcommand so tab-completion can fill in the
// full form. Order matches roughly what /help renders but groups
// subcommands under their parent for readability.
var slashCatalog = []slashEntry{
	{"/help", "show this list"},
	{"/new", "start a fresh session in this cwd"},
	{"/clear", "clear chat history"},
	{"/resume", "resume a specific session"},
	{"/sessions", "list saved sessions for this project"},
	{"/quit", "exit"},
	{"/logout", "clear saved credentials and exit (next launch re-runs first-time wizard)"},
	{"/stop", "stop the current generation"},
	{"/plan", "toggle plan/execute mode (same as Shift+Tab)"},
	{"/status", "connection + session info"},

	// Theme — name hints kept in the parent description so users
	// browsing the list can see them at a glance.
	{"/theme", "switch theme (dark/oak/forest/oled/light/…)"},
	{"/theme dark", "switch to dark theme"},
	{"/theme oak", "switch to oak theme"},
	{"/theme forest", "switch to forest theme"},
	{"/theme oled", "switch to oled theme"},
	{"/theme light", "switch to light theme"},

	// Tool approval mode — every variant tab-completable.
	{"/mode", "tool approval mode (auto/ask/locked/yolo/rules)"},
	{"/mode auto", "auto-approve all tools"},
	{"/mode ask", "prompt before each tool call"},
	{"/mode locked", "deny all tools (read-only chat)"},
	{"/mode yolo", "auto-approve EVERYTHING incl. dangerous"},
	{"/mode rules", "follow per-tool allow/deny rules"},
	{"/approve-all", "shortcut for /mode auto"},
	{"/approve-all-dangerous", "shortcut for /mode yolo"},

	// Background processes.
	{"/bg", "background process list / run / kill"},
	{"/bg list", "list background processes"},
	{"/bg run", "/bg run <command> — start a background process"},
	{"/bg kill", "/bg kill <id> — kill a background process"},

	{"/update", "check/install/list releases"},
	{"/update check", "check the stable channel for a newer release"},
	{"/update install", "install the latest stable release"},
	{"/update install pre", "install the latest pre-release (any kind)"},
	{"/update list", "list recent releases (stable + pre-release)"},

	// Background delegation policy. Backend lives in tools/executor.go;
	// see cmdDelegate in commands.go.
	{"/delegate", "configure background delegation policy"},
	{"/delegate default", "research+bg ok, orchestration stays local"},
	{"/delegate off", "no delegation at all"},
	{"/delegate research", "only parallel web research"},
	{"/delegate code", "research + parallel writes"},
	{"/delegate all", "unrestricted (old behavior)"},
	{"/delegate workers", "/delegate workers <n> — workers cap is server-side"},

	{"/context", "show the project context block"},
	{"/context refresh", "re-send project context on next message"},

	{"/tree", "print the project file tree (default depth 3)"},
	{"/init", "create SPORE.md template + add .spore-code/ to .gitignore"},

	// Panel visibility.
	{"/panel", "toggle the right-column activity panel"},
	{"/panel hide", "hide the activity panel"},
	{"/panel show", "show the activity panel"},
	{"/panel toggle", "toggle the activity panel"},

	// File-op sandbox.
	{"/scope", "file-op sandbox: strict (cwd) | expanded (any path)"},
	{"/scope strict", "lock file ops to cwd (default)"},
	{"/scope expanded", "allow file ops anywhere on this machine"},

	{"/test", "run a UI / behavior test (try /test list)"},
	{"/test list", "list available UI tests"},
	{"/test all", "run all UI tests"},

	// codeindex (v0.3.0+) — structural code search backed by the
	// per-project SQLite index at .spore-code/index.db.
	{"/index", "build/refresh the per-project code index (.spore-code/index.db)"},
	{"/index force", "rebuild every file regardless of mtime"},
	{"/architecture", "show clusters / hot paths / entry points / tech stack"},
	{"/arch", "alias for /architecture"},
	{"/why", "/why <symbol> — show callers of a symbol (depth 3)"},
	{"/calls", "/calls <symbol> — show callees of a symbol (depth 3)"},
	{"/impact", "show transitive caller blast-radius for current git diff"},
	{"/scripts", "/scripts [name] — list saved project scripts (graph-backed)"},
	{"/decisions", "list / create / get project decisions (ADRs) — graph-backed"},
	{"/decisions list", "list project decisions"},
	{"/decisions new", "/decisions new <title> — record a new ADR"},
	{"/decisions get", "/decisions get <id> — fetch a specific decision"},
}

// refreshSuggest recomputes matches for the current input buffer.
// Fires whenever the buffer starts with `/` and is itself a valid prefix
// of some catalog entry. That includes the "/cmd " case (parent + one
// trailing space) so subcommand lists pop up once the user finishes
// typing the parent — handy when you forget the option name.
func (m *Model) refreshSuggest() {
	raw := m.input.Value()
	// Don't trim — we need to detect the trailing-space case so
	// subcommand entries can surface after `/cmd `.
	text := strings.TrimLeft(raw, " ")
	if !strings.HasPrefix(text, "/") {
		m.suggest.visible = false
		m.suggest.matches = nil
		m.suggest.cursor = 0
		return
	}
	// If the user has typed past the first arg (two or more spaces, or
	// a space+non-empty arg), we hide — at that point the user is
	// composing arguments, not picking a subcommand.
	trimmed := strings.TrimRight(text, " ")
	tokens := strings.SplitN(trimmed, " ", 3)
	if len(tokens) >= 3 {
		m.suggest.visible = false
		m.suggest.matches = nil
		m.suggest.cursor = 0
		return
	}
	// `prefix` is what every shown entry must start with. For "/sc" it's
	// "/sc"; for "/scope " it's "/scope " (with the space) — so only
	// `/scope strict` / `/scope expanded` survive, not `/scope` itself.
	prefix := text
	out := make([]slashEntry, 0, len(slashCatalog))
	for _, e := range slashCatalog {
		if strings.HasPrefix(e.cmd, prefix) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].cmd) < len(out[j].cmd) })
	m.suggest.matches = out
	m.suggest.visible = len(out) > 0
	if m.suggest.cursor >= len(out) {
		m.suggest.cursor = 0
	}
}

// handleSuggestKey intercepts navigation / accept keys while the dropdown
// is open. Returns true if the key was consumed (don't forward to textarea).
//
// Key map:
//
//	↑ / shift+tab   move cursor up
//	↓               move cursor down
//	tab             FILL the highlighted suggestion into the buffer (no
//	                execute) — lets you tab to a parent then keep typing
//	                an arg, or tab to a subcommand variant and edit.
//	enter           ACCEPT + EXECUTE — fills and immediately runs.
//	esc             dismiss the dropdown
func (m *Model) handleSuggestKey(km tea.KeyMsg) (tea.Cmd, bool) {
	if !m.suggest.visible || len(m.suggest.matches) == 0 {
		return nil, false
	}
	switch km.String() {
	case "up", "shift+tab":
		m.suggest.cursor = (m.suggest.cursor - 1 + len(m.suggest.matches)) % len(m.suggest.matches)
		return nil, true
	case "down":
		m.suggest.cursor = (m.suggest.cursor + 1) % len(m.suggest.matches)
		return nil, true
	case "tab":
		// Fill and stay editing. Trailing space lets the user keep typing
		// args without re-positioning the cursor. Re-runs refreshSuggest
		// so subcommand entries surface immediately if the filled value
		// is a parent that has variants.
		e := m.suggest.matches[m.suggest.cursor]
		m.input.SetValue(e.cmd + " ")
		m.refreshSuggest()
		return nil, true
	case "enter":
		// Accept + execute. Fill the buffer with exactly the entry's
		// command (no trailing space — looks cleaner in the chat echo)
		// and fall through so updateKey's enter branch runs the slash
		// dispatch.
		e := m.suggest.matches[m.suggest.cursor]
		m.input.SetValue(e.cmd)
		m.suggest.visible = false
		m.suggest.matches = nil
		return nil, false // fall through to updateKey enter → handleSlashCommand
	case "esc":
		m.suggest.visible = false
		m.suggest.matches = nil
		return nil, true
	}
	return nil, false
}

// renderSuggest draws the dropdown above the input bar. width is the chat
// column width. Empty string if nothing to show.
func (m *Model) renderSuggest(width int) string {
	if !m.suggest.visible || len(m.suggest.matches) == 0 {
		return ""
	}
	max := 6
	if max > len(m.suggest.matches) {
		max = len(m.suggest.matches)
	}
	start := 0
	if m.suggest.cursor >= max {
		start = m.suggest.cursor - max + 1
	}
	var lines []string
	for i := start; i < start+max && i < len(m.suggest.matches); i++ {
		e := m.suggest.matches[i]
		cmd := e.cmd
		desc := e.desc
		if len(cmd) > 24 {
			cmd = cmd[:23] + "…"
		}
		row := cmd
		if desc != "" {
			padW := 24 - len(cmd)
			if padW < 1 {
				padW = 1
			}
			row += strings.Repeat(" ", padW) + lipgloss.NewStyle().Foreground(m.theme.Muted).Render(desc)
		}
		if i == m.suggest.cursor {
			row = lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true).Render("▸ " + row)
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}
	if len(m.suggest.matches) > max {
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Muted).Render(
			"  (+"+itoa(len(m.suggest.matches)-max)+" more — ↓ to scroll)"))
	}
	return borderStyle.Copy().
		BorderForeground(m.theme.Accent2).
		Foreground(m.theme.Fg).
		Padding(0, 1).
		Width(width - 2).
		Render(strings.Join(lines, "\n"))
}
