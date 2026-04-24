package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/yumlevi/acorn-cli/go/internal/config"
	"github.com/yumlevi/acorn-cli/go/internal/conn"
	"github.com/yumlevi/acorn-cli/go/internal/proto"
	"github.com/yumlevi/acorn-cli/go/internal/sessionlog"
)

// PlanPrefix — port of acorn/constants.py:PLAN_PREFIX. Prepended to the
// user's first plan-mode message.
const PlanPrefix = `[MODE: Plan only. You are in planning mode. Follow these phases in order:

PHASE 1 — ENVIRONMENT AUDIT:
The context above includes the local environment (OS, installed tools, runtimes). Review what is available. If the task requires tools/runtimes not installed, note them.

PHASE 2 — CODEBASE SCAN:
Use read_file, glob, and grep to understand the existing codebase structure, patterns, conventions, config files, and dependencies.

PHASE 3 — RESEARCH:
Identify topics you need more context on — frameworks, APIs, libraries, best practices. Use web_search and web_fetch to research them.

PHASE 4 — CLARIFY:
If you have questions for the user, you MUST use this EXACT format with the QUESTIONS: marker on its own line. Do NOT embed questions in the plan text.
QUESTIONS:
1. Single-select question? [Option A / Option B / Option C]
2. Multi-select question? {Option A / Option B / Option C / Option D}
3. Open-ended question?

If you have questions, output ONLY the QUESTIONS: block and STOP — do NOT include PLAN_READY in the same response. Wait for answers before presenting the plan.

PHASE 5 — PLAN:
Only after questions are answered (or if you have none), present a detailed plan with prerequisites, step-by-step changes with file paths, new files vs existing files to modify, dependencies to install, commands to run, and how to verify it works.

RULES:
- Do NOT make changes (no write_file, edit_file).
- Do NOT run destructive or modifying commands.
- You MAY use: read_file, glob, grep, web_search, web_fetch, exec (read-only commands only like ls, cat, which, --version).
- Do NOT put questions and PLAN_READY in the same response — ask first, then plan after answers.
- End your plan with "PLAN_READY" on its own line.]

`

// PlanExecuteMsg — port of acorn/constants.py:PLAN_EXECUTE_MSG.
const PlanExecuteMsg = `[The user has approved the plan above. Switch to execute mode and implement it now. Proceed step by step, executing all the changes you outlined.]`

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Off-thread messages: permissions layer asking to open a modal.
	if om, ok := msg.(openPermModalMsg); ok {
		m.modal = modalPermission
		m.permission = &permissionModal{
			name:      om.name,
			summary:   om.summary,
			rule:      om.rule,
			dangerous: om.dangerous,
		}
		return m, nil
	}

	// Modal intercept.
	if m.modal != modalNone {
		return m.updateModal(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg.Width, msg.Height)

	case tea.KeyMsg:
		return m.updateKey(msg)

	case connOpenMsg:
		first := !m.connected
		m.connected = true
		m.connErr = ""
		m.status = "connected"
		if first {
			m.pushChat("system", LogoFull)
		}
		m.pushChat("system", fmt.Sprintf("Connected to %s:%d as %s (session %s)",
			m.cfg.Connection.Host, m.cfg.Connection.Port, m.cfg.Connection.User, m.sess))
		_ = m.client.Send(map[string]any{
			"type": "chat:history-request", "sessionId": m.sess, "userName": m.cfg.Connection.User,
		})
		return m, nil

	case connErrorMsg:
		m.connected = false
		m.connErr = msg.err
		m.pushChat("system", "Connection error: "+msg.err)
		m.status = "disconnected"
		return m, nil

	case connClosedMsg:
		m.connected = false
		m.status = "disconnected"
		m.pushChat("system", "Disconnected.")
		return m, nil

	case wsFrameMsg:
		cmd := m.handleFrame(msg.frame)
		return m, tea.Batch(cmd, m.recvCmd())

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case sizePollMsg:
		// Belt-and-braces resize detection — Bubble Tea's Windows build
		// doesn't have a SIGWINCH equivalent so without this the layout
		// stays stuck at whatever size the terminal had on startup.
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			if w != m.width || h != m.height {
				mm, c := m.handleResize(w, h)
				return mm, tea.Batch(c, sizePollCmd())
			}
		}
		return m, sizePollCmd()

	case spinnerTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		// Keep ticking only while there's something to animate. Drops the
		// tick once generation ends so we don't burn cycles redrawing.
		if m.generating || m.thinking {
			return m, spinnerTickCmd()
		}
		return m, nil

	case toolHandledMsg:
		return m, m.toolCmd()

	case hookExecLineMsg:
		preview := msg.line
		if len(preview) > 120 {
			preview = preview[:120] + "…"
		}
		m.status = "⚙ " + preview
		m.outputLog = append(m.outputLog, msg.line)
		if len(m.outputLog) > 500 {
			m.outputLog = m.outputLog[len(m.outputLog)-500:]
		}
		return m, nil

	case hookCodeViewMsg:
		m.pushCodeView(msg.path, msg.content, msg.isNew)
		return m, nil

	case hookCodeDiffMsg:
		m.pushCodeDiff(msg.path, msg.oldT, msg.newT)
		return m, nil

	case hookToolDoneMsg:
		if m.writer != nil {
			m.writer.WriteTool(msg.name, msg.input, msg.result, true, msg.ms)
		}
		if m.dlog != nil {
			m.dlog.Info("tool", msg.name, "ms", msg.ms)
		}
		return m, nil

	case updateCheckResult:
		if msg.Err != "" {
			m.pushChat("system", "Update check failed: "+msg.Err)
			return m, nil
		}
		switch {
		case versionLE(msg.Version, Version):
			m.pushChat("system", fmt.Sprintf("You're on %s — that's the latest release.", Version))
		default:
			m.pushChat("system", fmt.Sprintf("Update available: %s → %s\n%s\n(run /update install to upgrade in place)",
				Version, msg.Version, msg.URL))
		}
		return m, nil

	case updateInstallResult:
		if msg.Err != "" {
			m.pushChat("system", "Update install failed: "+msg.Err)
			return m, nil
		}
		m.pushChat("system", fmt.Sprintf("Installed %s at %s — restart acorn to use the new binary.", msg.Version, msg.Path))
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Output log overlay — ctrl+o / esc closes; arrows scroll.
	if m.outputLogOpen {
		switch msg.String() {
		case "ctrl+o", "esc", "q":
			m.outputLogOpen = false
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		case "up":
			m.outputLogVP.LineUp(1)
			return m, nil
		case "down":
			m.outputLogVP.LineDown(1)
			return m, nil
		case "pgup":
			m.outputLogVP.LineUp(m.outputLogVP.Height - 2)
			return m, nil
		case "pgdown", " ":
			m.outputLogVP.LineDown(m.outputLogVP.Height - 2)
			return m, nil
		case "home", "g":
			m.outputLogVP.GotoTop()
			return m, nil
		case "end", "G":
			m.outputLogVP.GotoBottom()
			return m, nil
		}
		return m, nil
	}

	// Expanded side-panel — ctrl+p / esc / q close; arrows scroll.
	if m.panelExpand {
		switch msg.String() {
		case "ctrl+p", "esc", "q":
			m.panelExpand = false
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		case "up":
			m.panelView.LineUp(1)
			return m, nil
		case "down":
			m.panelView.LineDown(1)
			return m, nil
		case "pgup":
			m.panelView.LineUp(m.panelView.Height - 2)
			return m, nil
		case "pgdown", " ":
			m.panelView.LineDown(m.panelView.Height - 2)
			return m, nil
		case "home", "g":
			m.panelView.GotoTop()
			return m, nil
		case "end", "G":
			m.panelView.GotoBottom()
			return m, nil
		}
		return m, nil
	}

	// Slash autocomplete keys take priority when the dropdown is open.
	if _, consumed := m.handleSuggestKey(msg); consumed {
		return m, nil
	}

	// Up/Down command history. Only intercept when the textarea wouldn't
	// move the caret naturally — empty buffer or cursor already at the
	// first/last line. Keeps multi-line edits responsive.
	switch msg.String() {
	case "up":
		if m.handleHistoryNav(-1) {
			return m, nil
		}
	case "down":
		if m.handleHistoryNav(+1) {
			return m, nil
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m.handleCtrlC()
	case "ctrl+d":
		// Ctrl+D is the unconditional "yes really quit" key — same as
		// Python's process_manager.kill_all + exit. No double-tap.
		return m, m.shutdownCmd()

	case "shift+tab":
		m.planMode = !m.planMode
		label := "execute"
		if m.planMode {
			label = "plan"
		}
		m.pushChat("system", "Mode → "+label)
		return m, nil

	case "enter":
		// Alt+Enter / Ctrl+J insert a newline via textarea.KeyMap.InsertNewline
		// (rebound in model.go:New). Plain 'enter' arrives here as send.
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		if strings.HasPrefix(text, "/") {
			return m.handleSlashCommand(text)
		}
		m.input.Reset()
		// Record the send in command history before context-prefixing.
		// Skip identical-to-last to avoid noisy duplicates from re-sends.
		if n := len(m.cmdHistory); n == 0 || m.cmdHistory[n-1] != text {
			m.cmdHistory = append(m.cmdHistory, text)
			appendHistory(m.cfg.GlobalDir, text)
		}
		m.histIdx = -1
		m.histDraft = ""
		m.pushChat("user", text)

		content := text
		var projectCtx *proto.ProjectContext
		if m.serverCaps.ProjectContext {
			// New path: SPORE supports the projectContext sibling field.
			// Send the structured metadata fresh on every turn so the
			// server can route it into the system prompt — never glued
			// onto the user message, never accumulating in messages[].
			mode := "execute"
			if m.planMode {
				mode = "plan"
			}
			pc := BuildProjectContextWithScope(m.cwd, mode, m.scope)
			projectCtx = &pc
		} else {
			// Legacy fallback: SPORE didn't advertise projectContext
			// support so we glue the prose blob onto the first message
			// and prepend PlanPrefix in plan mode, exactly like before.
			if !m.contextSent {
				content = GatherContext(m.cwd) + "\n\n" + content
				m.contextSent = true
			}
			if m.planMode {
				content = PlanPrefix + content
			}
		}
		m.generating = true
		m.status = "waiting…"
		m.thinkingTokens = 0
		return m, tea.Batch(m.sendChat(content, text, projectCtx), spinnerTickCmd())

	case "pgup":
		m.viewport.LineUp(m.viewport.Height - 2)
		m.followBottom = false
		return m, nil
	case "pgdown":
		m.viewport.LineDown(m.viewport.Height - 2)
		m.updateFollowBottom()
		return m, nil
	case "ctrl+up", "shift+up":
		// Belt-and-braces scroll for terminals that swallow PgUp/PgDn or
		// don't deliver mouse wheel events (some Windows console hosts).
		m.viewport.LineUp(1)
		m.followBottom = false
		return m, nil
	case "ctrl+down", "shift+down":
		m.viewport.LineDown(1)
		m.updateFollowBottom()
		return m, nil
	case "ctrl+home":
		m.viewport.GotoTop()
		m.followBottom = false
		return m, nil
	case "ctrl+end":
		m.viewport.GotoBottom()
		m.followBottom = true
		return m, nil
	case "ctrl+p":
		m.panelExpand = !m.panelExpand
		return m, nil
	case "ctrl+o":
		m.outputLogOpen = !m.outputLogOpen
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Re-compute slash suggestions after every keystroke that reached the
	// textarea. Cheap lookup over a ~15-item catalog.
	m.refreshSuggest()
	return m, cmd
}

// handleHistoryNav cycles through cmdHistory when the textarea wouldn't
// otherwise consume the arrow key (empty input, or caret on edge line).
// dir = -1 for Up, +1 for Down. Returns true if the event was consumed.
func (m *Model) handleHistoryNav(dir int) bool {
	if len(m.cmdHistory) == 0 {
		return false
	}
	val := m.input.Value()
	line := m.input.Line()
	lc := m.input.LineCount()
	emptyOrEdge := val == "" ||
		(dir < 0 && line == 0) ||
		(dir > 0 && line >= lc-1)
	if !emptyOrEdge {
		return false
	}
	if m.histIdx == -1 {
		// First Up press: stash the in-progress draft and jump to newest.
		if dir < 0 {
			m.histDraft = val
			m.histIdx = len(m.cmdHistory) - 1
		} else {
			// Down with no history browse in flight is a no-op so the
			// textarea can keep handling the key.
			return false
		}
	} else {
		next := m.histIdx + dir
		if next < 0 {
			// Past the oldest — stay put.
			return true
		}
		if next >= len(m.cmdHistory) {
			// Past the newest — restore the draft.
			m.histIdx = -1
			m.input.SetValue(m.histDraft)
			m.input.CursorEnd()
			return true
		}
		m.histIdx = next
	}
	m.input.SetValue(m.cmdHistory[m.histIdx])
	m.input.CursorEnd()
	return true
}

// handleCtrlC mirrors acorn/app.py:action_quit_check.
//
//   - generating: stop the in-flight turn (send chat:stop, abort any
//     local exec, flush partial stream). Single tap, no quit.
//   - idle, first tap: arm a 1s "press again to quit" window.
//   - idle, second tap within 1s: shut down cleanly.
//
// Ctrl+D remains the no-prompt eject hatch.
func (m *Model) handleCtrlC() (tea.Model, tea.Cmd) {
	now := time.Now()
	if m.generating || m.thinking {
		// Tell the server to abort.
		if m.client != nil {
			_ = m.client.Send(map[string]any{"type": "chat:stop", "sessionId": m.sess})
		}
		// Cancel any in-flight local tool exec.
		if m.exec != nil {
			m.exec.AbortCurrent()
		}
		// End the streaming entry so it stops looking like it's still going.
		if m.currentStreamIdx >= 0 {
			m.endStream()
		}
		m.generating = false
		m.thinking = false
		m.thinkingTokens = 0
		m.status = ""
		m.pushChat("system", "⏹ Stopped")
		m.lastCtrlC = now
		return m, nil
	}
	// Idle path — double-tap to confirm.
	if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) < time.Second {
		return m, m.shutdownCmd()
	}
	m.lastCtrlC = now
	m.pushChat("system", "Press Ctrl+C again to quit")
	return m, nil
}

// shutdownCmd is the actual exit sequence shared by ctrl+d, /quit, and
// the second ctrl+c tap — kill BG procs, close the WS, flush logs.
func (m *Model) shutdownCmd() tea.Cmd {
	if m.exec != nil && m.exec.BG != nil {
		m.exec.BG.KillAll()
	}
	if m.client != nil {
		m.client.Close()
	}
	if m.dlog != nil {
		m.dlog.Close()
	}
	if m.writer != nil {
		m.writer.Close()
	}
	return tea.Quit
}

// handleMouse routes wheel events to whichever viewport currently has
// the user's attention. Wheel scrolling on the chat viewport flips
// followBottom so the auto-scroll-to-bottom in rerenderViewport stops
// fighting the user.
func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	const wheelStep = 3
	switch {
	case m.outputLogOpen:
		switch msg.Type {
		case tea.MouseWheelUp:
			m.outputLogVP.LineUp(wheelStep)
		case tea.MouseWheelDown:
			m.outputLogVP.LineDown(wheelStep)
		}
		return m, nil
	case m.panelExpand:
		switch msg.Type {
		case tea.MouseWheelUp:
			m.panelView.LineUp(wheelStep)
		case tea.MouseWheelDown:
			m.panelView.LineDown(wheelStep)
		}
		return m, nil
	}
	switch msg.Type {
	case tea.MouseWheelUp:
		m.viewport.LineUp(wheelStep)
		m.followBottom = false
	case tea.MouseWheelDown:
		m.viewport.LineDown(wheelStep)
		m.updateFollowBottom()
	}
	return m, nil
}

// updateFollowBottom recomputes followBottom from the viewport's
// current scroll position. Called after any user-initiated scroll
// down so reaching the last line re-engages auto-follow.
func (m *Model) updateFollowBottom() {
	max := m.viewport.TotalLineCount() - m.viewport.Height
	if max < 0 {
		max = 0
	}
	m.followBottom = m.viewport.YOffset >= max
}

// handleResize is the single source of truth for terminal-size changes.
// Every cached/lazy widget that depends on dimensions is force-reset
// here so the next View() rebuilds at the new size, and a ClearScreen
// is queued so the alt-screen buffer doesn't keep stale glyphs from a
// shrink resize.
func (m *Model) handleResize(w, h int) (tea.Model, tea.Cmd) {
	m.width, m.height = w, h
	// Force the chat-history cache to rebuild at the new width, even if
	// viewport.Width happens to equal historyWidth (it shouldn't, but the
	// belt-and-braces protects against rounding edge cases when side
	// panels appear/disappear at the resize boundary).
	m.historyDirty = true
	m.historyWidth = -1
	m.input.SetWidth(w - 2)
	// Discard the stateful overlay viewports — they'll re-init at the
	// new innerW/innerH on next View(). Cheaper than trying to keep
	// scroll position consistent across a resize.
	m.panelViewInit = false
	m.outputLogInit = false
	// One re-render here so View() doesn't have to figure out two
	// invalidations in a row.
	m.rerenderViewport()
	return m, tea.ClearScreen
}

func (m *Model) handleSlashCommand(text string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return m, nil
	}
	// Registry first — phase-7 commands (/context, /tree, /init) live there.
	if mm, c, ok := dispatchSlash(m, text); ok {
		return mm, c
	}
	cmd := parts[0]

	switch cmd {
	case "/help":
		m.pushChat("system", SlashHelp())
	case "/clear":
		m.messages = m.messages[:0]
		m.historyDirty = true
		m.rerenderViewport()
		_ = m.client.Send(map[string]any{"type": "chat:clear", "sessionId": m.sess})
	case "/new":
		prev := m.sess
		// Always use the fresh (timestamped) variant so the new session
		// is genuinely distinct on the server side, even when AutoResume
		// is on. The old session is preserved on disk + server and stays
		// reachable via `/resume <id>` or `acorn -c`.
		m.sess = ComputeSessionIDFresh(m.cfg.Connection.User, m.cwd)
		m.messages = m.messages[:0]
		m.contextSent = false
		m.historyDirty = true
		// Rotate the JSONL session writer + debug log so the new turns
		// land in their own files. Without this, /new conversations
		// would keep appending to the old session's log.
		if m.writer != nil {
			m.writer.Close()
		}
		if w, err := sessionlog.Open(m.cfg.GlobalDir, m.sess); err == nil {
			m.writer = w
		} else {
			m.writer = nil
		}
		if m.dlog != nil {
			m.dlog.Close()
		}
		m.dlog = sessionlog.OpenDebug(m.cfg.GlobalDir, m.sess, m.cfg.Connection.User, m.cwd)
		m.rerenderViewport()
		m.pushChat("system", "New session: "+m.sess)
		if prev != "" && prev != m.sess {
			m.pushChat("system", "Previous session preserved: "+prev+"  (use /resume "+prev+" to return)")
		}
	case "/resume":
		if len(parts) < 2 {
			m.pushChat("system", "Usage: /resume <sessionId>")
			return m, nil
		}
		m.sess = parts[1]
		m.messages = m.messages[:0]
		m.historyDirty = true
		// Load local JSONL history first so the user sees their previous
		// turns even if the server doesn't know the session. Server's
		// chat:history reply (if any) will append after.
		m.loadLocalHistory()
		_ = m.client.Send(map[string]any{"type": "chat:history-request", "sessionId": m.sess, "userName": m.cfg.Connection.User})
		m.pushChat("system", "Resumed: "+m.sess)
	case "/quit":
		if m.exec != nil && m.exec.BG != nil {
			m.exec.BG.KillAll()
		}
		m.client.Close()
		if m.dlog != nil {
			m.dlog.Close()
		}
		if m.writer != nil {
			m.writer.Close()
		}
		return m, tea.Quit
	case "/stop":
		m.exec.AbortCurrent()
		_ = m.client.Send(map[string]any{"type": "chat:stop", "sessionId": m.sess})
		m.pushChat("system", "Stop requested.")
	case "/plan":
		m.planMode = !m.planMode
		label := "execute"
		if m.planMode {
			label = "plan"
		}
		m.pushChat("system", "Mode → "+label)
	case "/status":
		m.pushChat("system", fmt.Sprintf("server=%s:%d user=%s session=%s planMode=%t mode=%s",
			m.cfg.Connection.Host, m.cfg.Connection.Port, m.cfg.Connection.User, m.sess, m.planMode, m.perms.Mode()))
	case "/theme":
		if len(parts) >= 2 {
			m.theme = themeForName(parts[1])
			// Persist so the next `acorn` launch comes up in the
			// same theme. Update the in-memory cfg first then write
			// the global config.toml. Save errors aren't fatal —
			// the theme still applies for this session.
			m.cfg.Display.Theme = m.theme.Name
			if err := config.Save(m.cfg); err != nil {
				m.pushChat("system", "Theme → "+m.theme.Name+"  (save failed: "+err.Error()+")")
			} else {
				m.pushChat("system", "Theme → "+m.theme.Name+"  (saved)")
			}
			m.historyDirty = true
			m.rerenderViewport()
		} else {
			m.pushChat("system", "Current: "+m.theme.Name+"\nAvailable: "+strings.Join(ThemeNames(), ", "))
		}
	case "/mode":
		if len(parts) < 2 {
			m.pushChat("system", "Usage: /mode <auto|ask|locked|yolo|rules>")
			return m, nil
		}
		switch parts[1] {
		case "auto":
			m.perms.SetMode(PermAuto)
			m.pushChat("system", "Perms → auto (non-dangerous auto-approved)")
		case "ask":
			m.perms.SetMode(PermAsk)
			m.pushChat("system", "Perms → ask (prompt per call)")
		case "locked":
			m.perms.SetMode(PermLocked)
			m.pushChat("system", "Perms → locked (deny all writes/exec)")
		case "yolo":
			m.perms.SetMode(PermYolo)
			m.pushChat("system", "Perms → yolo (approve everything)")
		case "rules":
			rs := m.perms.Rules()
			if len(rs) == 0 {
				m.pushChat("system", "No session allow rules")
			} else {
				m.pushChat("system", "Allow rules:\n"+strings.Join(rs, "\n"))
			}
		default:
			m.pushChat("system", "Unknown mode: "+parts[1])
		}
	case "/approve-all":
		m.perms.SetMode(PermAuto)
		m.pushChat("system", "Perms → auto")
	case "/approve-all-dangerous":
		m.perms.SetMode(PermYolo)
		m.pushChat("system", "Perms → yolo")
	case "/update":
		switch {
		case len(parts) >= 2 && parts[1] == "install":
			tag := ""
			if len(parts) >= 3 {
				tag = parts[2]
			}
			m.pushChat("system", "Installing latest release… will replace the running binary in place.")
			return m, installUpdateCmd(tag)
		case len(parts) >= 2 && parts[1] == "check":
			m.pushChat("system", "Checking GitHub releases…")
			return m, checkUpdateCmd(true)
		default:
			m.pushChat("system", "Usage: /update check | /update install [version]")
			return m, nil
		}
	case "/bg":
		if m.exec == nil || m.exec.BG == nil {
			m.pushChat("system", "Background manager not available")
			return m, nil
		}
		if len(parts) < 2 || parts[1] == "list" {
			procs := m.exec.BG.List()
			if len(procs) == 0 {
				m.pushChat("system", "No background processes")
				return m, nil
			}
			var lines []string
			for _, p := range procs {
				st := "running"
				if !p.Running {
					st = fmt.Sprintf("exited (%d)", p.ExitCode)
				}
				cmd := p.Command
				if len(cmd) > 80 {
					cmd = cmd[:80]
				}
				lines = append(lines, fmt.Sprintf("  #%d  %s  %s  %s", p.ID, st, p.Elapsed(), cmd))
			}
			m.pushChat("system", strings.Join(lines, "\n"))
			return m, nil
		}
		sub := parts[1]
		if sub == "kill" && len(parts) >= 3 {
			var id int
			fmt.Sscanf(parts[2], "%d", &id)
			if m.exec.BG.Kill(id) {
				m.pushChat("system", fmt.Sprintf("Killed #%d", id))
			} else {
				m.pushChat("system", fmt.Sprintf("Process #%d not found or already stopped", id))
			}
			return m, nil
		}
		if sub == "run" && len(parts) >= 3 {
			cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "/bg"), " run"))
			if p, err := m.exec.BG.Launch(cmd, m.cwd); err != nil {
				m.pushChat("system", "Launch failed: "+err.Error())
			} else {
				m.pushChat("system", fmt.Sprintf("Launched #%d — log: %s", p.ID, p.LogPath))
			}
			return m, nil
		}
		// /bg <id> — show output
		var id int
		if _, err := fmt.Sscanf(sub, "%d", &id); err == nil {
			p := m.exec.BG.Get(id)
			if p == nil {
				m.pushChat("system", fmt.Sprintf("Process #%d not found", id))
				return m, nil
			}
			out := strings.Join(p.Output(), "\n")
			if len(out) > 4000 {
				out = "…" + out[len(out)-4000:]
			}
			m.pushChat("system", fmt.Sprintf("#%d  %s\n%s", p.ID, p.Elapsed(), out))
			return m, nil
		}
		m.pushChat("system", "Usage: /bg [list|<id>|run <command>|kill <id>]")
	case "/sessions":
		root := findGitRoot(m.cwd)
		if root == "" {
			root = m.cwd
		}
		list := sessionlog.ListProjectSessions(m.cfg.GlobalDir, m.cfg.Connection.User, root)
		if len(list) == 0 {
			m.pushChat("system", "No saved sessions for this project")
			return m, nil
		}
		var lines []string
		lines = append(lines, fmt.Sprintf("Sessions for this project (%d):", len(list)))
		for i, s := range list {
			if i >= 15 {
				break
			}
			lines = append(lines, fmt.Sprintf("  %2d. %-12s %3d msgs  %s", i+1, s.TimeAgo, s.MessageCount, truncateFor(s.Preview, 60)))
		}
		lines = append(lines, "", "/resume <sessionId> to pick one")
		m.pushChat("system", strings.Join(lines, "\n"))
	default:
		m.pushChat("system", "Unknown command: "+cmd+"  (type /help)")
	}
	return m, nil
}

func (m *Model) handleFrame(f conn.Frame) tea.Cmd {
	switch f.Type {
	case "capabilities":
		// SPORE advertises supported features after the WS upgrade.
		// We use this to decide whether to send projectContext as a
		// sibling field (new path) or fall back to gluing GatherContext
		// onto the message content (old path).
		var caps proto.ServerCapabilities
		if err := json.Unmarshal(f.Raw, &caps); err == nil {
			m.serverCaps = caps
			if caps.ProjectContext && m.dlog != nil {
				m.dlog.Info("caps", "projectContext supported by SPORE "+caps.SporeVersion)
			}
		}
	case "chat:start":
		// Don't pre-create the bubble — appendDelta starts one
		// lazily on the first non-empty delta. If the agent goes
		// straight to a tool without any text, no empty bordered
		// box appears in the transcript.
		m.thinkingBuf = ""
	case "chat:delta":
		var v proto.ChatDelta
		_ = json.Unmarshal(f.Raw, &v)
		m.appendDelta(v.Text)
	case "chat:done":
		m.endStream()
		m.generating = false
		m.status = ""
		m.postStreamChecks()
	case "chat:thinking":
		var v proto.ChatThinking
		_ = json.Unmarshal(f.Raw, &v)
		m.status = "thinking…"
		if v.Text != "" {
			m.thinkingBuf += v.Text
			m.appendThinking(v.Text)
		}
	case "chat:status":
		var v proto.ChatStatus
		_ = json.Unmarshal(f.Raw, &v)
		m.handleStatus(v)
	case "chat:tool":
		var v proto.ChatTool
		_ = json.Unmarshal(f.Raw, &v)
		n := v.Name
		if n == "" {
			n = v.Tool
		}
		if n != "" {
			m.status = "⚙ " + n
		}
	case "chat:history":
		var v proto.ChatHistory
		_ = json.Unmarshal(f.Raw, &v)
		// If local JSONL already populated history, skip duplicate server
		// entries (naive: match by (role, first 120 chars of text)). Keeps
		// /resume from showing each prior turn twice.
		seen := make(map[string]struct{}, len(m.messages))
		for _, existing := range m.messages {
			seen[historyKey(existing.Role, existing.Text)] = struct{}{}
		}
		added := 0
		for _, h := range v.Messages {
			role := h.Role
			if role != "user" && role != "assistant" {
				role = "system"
			}
			if _, dup := seen[historyKey(role, h.Text)]; dup {
				continue
			}
			m.messages = append(m.messages, chatMsg{Role: role, Text: h.Text})
			added++
		}
		if added > 0 {
			m.historyDirty = true
			m.rerenderViewport()
		}
	case "chat:error":
		var v proto.ChatError
		_ = json.Unmarshal(f.Raw, &v)
		m.pushChat("system", "chat error: "+v.Error)
		m.generating = false
		m.status = ""
	case "chat:busy":
		m.pushChat("system", "Server: session busy (another client may be running it)")
	case "code:view":
		var v proto.CodeView
		_ = json.Unmarshal(f.Raw, &v)
		m.pushCodeView(v.Path, v.Content, v.IsNew)
	case "code:diff":
		var v proto.CodeDiff
		_ = json.Unmarshal(f.Raw, &v)
		m.pushCodeDiff(v.Path, v.OldText, v.NewText)
	case "ask_user":
		var v proto.AskUser
		_ = json.Unmarshal(f.Raw, &v)
		m.openStructuredQuestion(v)
	case "plan_proposal":
		var v proto.PlanProposal
		_ = json.Unmarshal(f.Raw, &v)
		m.pushChat("system", fmt.Sprintf("[plan] queued #%d: %s — %s", v.ProposalID, v.Tool, v.Summary))
	case "plan_applied":
		var v proto.PlanApplied
		_ = json.Unmarshal(f.Raw, &v)
		for _, r := range v.Results {
			mark := "✓"
			if !r.OK {
				mark = "✗"
			}
			m.pushChat("system", fmt.Sprintf("[plan] %s %s %s", mark, r.Tool, r.Summary))
		}
	case "plan_rejected":
		m.pushChat("system", "[plan] proposals rejected")
	case "plan_mode":
		var v proto.PlanMode
		_ = json.Unmarshal(f.Raw, &v)
		m.planMode = v.Enabled
		label := "execute"
		if m.planMode {
			label = "plan"
		}
		m.pushChat("system", "Mode → "+label+" (remote)")
	case "plan:decision":
		// Mobile observer pressed execute/revise/cancel — resolve the
		// local modal if open.
		var v struct {
			Type     string `json:"type"`
			Action   string `json:"action"`
			Feedback string `json:"feedback,omitempty"`
		}
		_ = json.Unmarshal(f.Raw, &v)
		if m.modal == modalPlan && m.planApproval != nil {
			text := m.planApproval.text
			switch v.Action {
			case "execute":
				m.pushChat("system", "→ Execute (from mobile)")
				m2, cmd := m.planExecute(text)
				_ = m2
				return cmd
			case "revise":
				m.pushChat("system", "→ Revise (from mobile)")
				m2, cmd := m.planReviseWithFeedback(v.Feedback)
				_ = m2
				return cmd
			case "cancel":
				m.modal = modalNone
				m.planApproval = nil
				m.pushChat("system", "→ Cancel (from mobile)")
			}
		}
	case "perm:query":
		// Observer joined; reply with full interactive state so mobile
		// can render the same sheets we have open.
		m.Broadcast("perm:current-mode", map[string]any{"mode": string(m.perms.Mode())})
		m.Broadcast("plan:set-mode", map[string]any{"enabled": m.planMode})
		if m.modal == modalPlan && m.planApproval != nil {
			preview := m.planApproval.text
			if len(preview) > 2000 {
				preview = preview[:2000]
			}
			m.Broadcast("plan:show-approval", map[string]any{"text": preview})
		}
		if m.modal == modalQuestion && m.question != nil && m.question.source == "prose" {
			items := make([]map[string]any, 0, len(m.question.questions))
			for i, q := range m.question.questions {
				item := map[string]any{"text": q.Text, "multi": q.Multi, "index": i + 1}
				if q.Options != nil {
					item["options"] = q.Options
				}
				items = append(items, item)
			}
			m.Broadcast("state:questions", map[string]any{"questions": items})
		}
	case "perm:set-mode":
		var v struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(f.Raw, &v)
		if v.Mode != "" {
			m.perms.SetMode(PermMode(v.Mode))
			m.pushChat("system", "Perms → "+v.Mode+" (from mobile)")
		}
	case "plan:decided", "plan:set-mode",
		"plan:show-approval", "interactive:resolved",
		"delegate:config", "tool:awaiting-approval",
		"state:questions", "perm:current-mode":
		// observer relays — outbound only here (we send these to observers)
	case "conn:error":
		// already surfaced via connErrorMsg path

	default:
		// Subagent activity — the server streams subagent:start / :line /
		// :done / :error with a taskId. Route into the side panel.
		if strings.HasPrefix(f.Type, "subagent:") {
			verb := strings.TrimPrefix(f.Type, "subagent:")
			var raw map[string]any
			_ = json.Unmarshal(f.Raw, &raw)
			m.handleSubagentFrame(verb, raw)
		}
	}
	return nil
}

func (m *Model) handleStatus(v proto.ChatStatus) {
	switch v.Status {
	case "thinking_start":
		// Close out any open assistant bubble so the thinking marker —
		// and whatever new bubble follows after thinking_done — render
		// as separate panels in the transcript instead of getting
		// glued onto the previous text.
		m.endStream()
		m.status = "thinking…"
		m.thinking = true
		m.thinkingTokens = 0
		m.thinkingBuf = ""
	case "thinking", "thinking_token":
		if v.Tokens > 0 {
			m.thinkingTokens = v.Tokens
		} else if v.Count > 0 {
			m.thinkingTokens = v.Count
		} else {
			m.thinkingTokens++
		}
	case "thinking_done":
		m.status = ""
		m.thinking = false
		// Don't dump the thought into the main transcript — the
		// activity panel already shows the live 💭 entry. Keeping
		// the chat clean was an explicit user ask.
		m.thinkingBuf = ""
	case "tool_exec_start":
		// Flush the in-flight assistant bubble so the user sees a
		// clean break before the tool indicator. The next chat:delta
		// will start a fresh bubble. Mirrors Python's
		// flush_stream_buffer() call in on_status.
		m.endStream()
		m.status = fmt.Sprintf("⚙ %s %s", v.Tool, v.Detail)
		if v.Tool != "" {
			detail := v.Detail
			if detail != "" {
				m.pushChat("system", fmt.Sprintf("⚙ %s · %s", v.Tool, truncateForLog(detail, 120)))
			} else {
				m.pushChat("system", "⚙ "+v.Tool)
			}
			m.appendToolExec(v.Tool, v.Detail)
		}
	case "tool_exec_done":
		m.status = fmt.Sprintf("✓ %s · %dms", v.Tool, v.DurationMs)
		// Inline 'tool done' indicator — duration + result size if
		// the server reported them. Same shape as Python's
		// '✓ Nms · Nchars' line.
		var parts []string
		if v.DurationMs > 0 {
			parts = append(parts, fmt.Sprintf("%dms", v.DurationMs))
		}
		if v.ResultChars > 0 {
			parts = append(parts, fmt.Sprintf("%d chars", v.ResultChars))
		}
		tail := ""
		if len(parts) > 0 {
			tail = " · " + strings.Join(parts, " · ")
		}
		m.pushChat("system", fmt.Sprintf("  ✓ %s%s", v.Tool, tail))
	case "interjected", "interjection":
		m.status = "interjecting…"
	case "waiting":
		m.status = "waiting…"
	case "truncated":
		m.pushChat("system", "[agent] response hit max_tokens — retrying with smaller output")
	}
}

// truncateForLog clips an arbitrary string to n characters with an
// ellipsis. Used for the '⚙ tool · detail' inline indicator so a
// long shell command doesn't blow out the panel width.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// postStreamChecks runs after chat:done to detect QUESTIONS: / PLAN_READY.
func (m *Model) postStreamChecks() {
	if m.currentStreamIdx >= 0 || len(m.messages) == 0 {
		return
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != "assistant" {
		return
	}
	hasPlan := m.planMode && strings.Contains(last.Text, "PLAN_READY")
	if hasPlan {
		m.stashedPlan = last.Text
	}
	if qs := parseQuestionsBlock(last.Text); qs != nil {
		m.openQuestionModal(qs)
		return
	}
	if hasPlan {
		m.openPlanModal(m.stashedPlan)
		m.stashedPlan = ""
	}
}

// truncateFor is a mini helper for log-line output (view.go has its own).
func truncateFor(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// SlashHelp returns the command help block.
func SlashHelp() string {
	return strings.Join([]string{
		"/help — this list",
		"/new — start a fresh session in this cwd",
		"/clear — clear chat history (server-side too)",
		"/resume <sessionId> — resume a specific session",
		"/quit — exit",
		"/stop — stop the current generation",
		"/plan — toggle plan/execute mode (same as Shift+Tab)",
		"/status — connection info",
		"/theme <name> — switch theme (dark, oak, forest, oled, light)",
		"/mode <auto|ask|locked|yolo|rules> — tool approval mode",
		"/approve-all — shortcut for /mode auto",
		"/approve-all-dangerous — shortcut for /mode yolo",
		"/sessions — list saved sessions for this project",
		"/context — show project context block (refresh: re-send next turn)",
		"/tree [depth] — print the project file tree",
		"/init — create ACORN.md + add .acorn/ to .gitignore",
		"/panel [hide|show|toggle] — toggle the right-column activity panel",
		"/scope [strict|expanded] — file-op sandbox (strict=cwd only, expanded=any path)",
		"/test [list|all|<name>] — exercise UI features without an agent round-trip",
	}, "\n")
}
