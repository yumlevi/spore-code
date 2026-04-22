package app

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

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

	case updateCheckResult:
		if msg.Err != "" {
			m.pushChat("system", "Update check failed: "+msg.Err)
			return m, nil
		}
		m.pushChat("system", fmt.Sprintf("Latest release: %s — %s\n(run /update install to replace this binary)", msg.Version, msg.URL))
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
	case "ctrl+c", "ctrl+d":
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

	case "shift+tab":
		m.planMode = !m.planMode
		label := "execute"
		if m.planMode {
			label = "plan"
		}
		m.pushChat("system", "Mode → "+label)
		return m, nil

	case "enter":
		if msg.Alt {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
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
		if !m.contextSent {
			content = GatherContext(m.cwd) + "\n\n" + content
			m.contextSent = true
		}
		if m.planMode {
			content = PlanPrefix + content
		}
		m.generating = true
		m.status = "waiting…"
		m.thinkingTokens = 0
		return m, tea.Batch(m.sendChat(content), spinnerTickCmd())

	case "pgup":
		m.viewport.LineUp(m.viewport.Height - 2)
		return m, nil
	case "pgdown":
		m.viewport.LineDown(m.viewport.Height - 2)
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
		m.rerenderViewport()
		_ = m.client.Send(map[string]any{"type": "chat:clear", "sessionId": m.sess})
	case "/new":
		m.sess = ComputeSessionID(m.cfg.Connection.User, m.cwd)
		m.messages = m.messages[:0]
		m.contextSent = false
		m.rerenderViewport()
		m.pushChat("system", "New session: "+m.sess)
	case "/resume":
		if len(parts) < 2 {
			m.pushChat("system", "Usage: /resume <sessionId>")
			return m, nil
		}
		m.sess = parts[1]
		m.messages = m.messages[:0]
		m.rerenderViewport()
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
			m.pushChat("system", "Theme → "+m.theme.Name)
			m.rerenderViewport()
		} else {
			m.pushChat("system", "Themes: "+strings.Join(ThemeNames(), ", "))
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
	case "chat:start":
		m.startStream()
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
		m.status = "thinking…"
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
		for _, h := range v.Messages {
			role := h.Role
			if role != "user" && role != "assistant" {
				role = "system"
			}
			m.messages = append(m.messages, chatMsg{Role: role, Text: h.Text})
		}
		m.rerenderViewport()
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
		m.status = "thinking…"
		m.thinking = true
		m.thinkingTokens = 0
	case "thinking", "thinking_token":
		// Server may stream a per-token tick with the running count.
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
	case "tool_exec_start":
		m.status = fmt.Sprintf("⚙ %s %s", v.Tool, v.Detail)
	case "tool_exec_done":
		m.status = fmt.Sprintf("✓ %s · %dms", v.Tool, v.DurationMs)
	case "interjected", "interjection":
		m.status = "interjecting…"
	case "waiting":
		m.status = "waiting…"
	case "truncated":
		m.pushChat("system", "[agent] response hit max_tokens — retrying with smaller output")
	}
}

// postStreamChecks runs after chat:done to detect QUESTIONS: / PLAN_READY.
func (m *Model) postStreamChecks() {
	if m.currentStream != nil || len(m.messages) == 0 {
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
	}, "\n")
}
