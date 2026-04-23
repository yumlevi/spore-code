package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/acorn-cli/go/internal/config"
	"github.com/yumlevi/acorn-cli/go/internal/conn"
	"github.com/yumlevi/acorn-cli/go/internal/proto"
	"github.com/yumlevi/acorn-cli/go/internal/sessionlog"
	"github.com/yumlevi/acorn-cli/go/internal/tools"
)

type modalKind int

const (
	modalNone modalKind = iota
	modalQuestion
	modalPlan
	modalPermission
)

// chatMsg is one panel in the chat log.
type chatMsg struct {
	Role      string // "user" | "assistant" | "system" | "tool"
	Text      string
	Timestamp time.Time
	Streaming bool
}

// Model is the Bubble Tea Model.
type Model struct {
	cfg  *config.Config
	cwd  string
	sess string

	client  *conn.Client
	exec    *tools.Executor
	perms   *TUIPerms

	connected bool
	connErr   string

	messages      []chatMsg
	// currentStreamIdx is the index in m.messages of the chatMsg
	// currently receiving chat:delta chunks, or -1 if no stream is
	// open. Indexing — rather than keeping a *chatMsg pointer — is
	// load-bearing: pushChat for ⚙/✓ tool indicators may grow
	// m.messages past its capacity, reallocating the backing array
	// and invalidating any held pointers. With an index we just
	// re-resolve &m.messages[idx] on every access.
	currentStreamIdx int
	viewport         viewport.Model
	input         textarea.Model
	width, height int

	planMode     bool
	contextSent  bool // gather_context only on first message
	generating   bool
	status       string
	theme        Theme

	// Modals
	modal        modalKind
	question     *questionModal
	planApproval *planModal
	permission   *permissionModal

	// Plan text stashed by ws_events while processing QUESTIONS: in plan mode
	// — same composition fix as the Python 277fc8c commit.
	stashedPlan string

	// sendProgramMsg is wired by main.go after tea.NewProgram so off-thread
	// goroutines (the permissions blocking prompt) can poke the UI.
	sendProgramMsg func(msg tea.Msg)

	// Session writer — appends every user/assistant/tool turn to a JSONL
	// file under ~/.acorn/sessions/ for crash recovery + /resume picker.
	writer *sessionlog.Writer

	// Diagnostic log at ~/.acorn/logs/<ts>_<session>.log — matches Python's
	// session_log.py output for parity across acorn variants.
	dlog *sessionlog.DebugLogger

	// Side panel state for code viewer + subagent activity.
	codeViews    []codeViewEntry
	subagents    *subagentPanel
	panelExpand  bool               // ctrl+p opens a full-height browser
	panelView    viewport.Model     // scrollable viewport for the expanded panel
	panelViewInit bool

	// Slash-command autocomplete state.
	suggest slashSuggest

	// Streaming optimization. renderedHistory caches the lipgloss output
	// for every message EXCEPT the one currently streaming, so chat:delta
	// updates only re-render the tail. tailDirty marks the current
	// streaming block as needing a re-render on next View().
	renderedHistory string
	historyDirty    bool
	historyWidth    int // width the cache was rendered against

	// followBottom — when true, every render snaps the viewport to the
	// last line so streaming text stays visible. Set to false the moment
	// the user scrolls up so they can read history without it being
	// yanked back. Set true again when they scroll all the way down.
	followBottom bool

	// Command history — Up/Down in input cycles through prior sends.
	// Persisted to ~/.acorn/history (plain text, one entry per line) so
	// it survives restarts. Mirrors prompt_toolkit's FileHistory.
	cmdHistory  []string
	histIdx     int    // -1 = not browsing; 0..len-1 = position
	histDraft   string // saved in-progress text when user starts browsing

	// Activity indicators for the header.
	thinkingTokens int  // running token count during a thinking turn
	spinnerFrame   int  // index into spinnerFrames for animated activity dot
	thinking       bool // true between thinking_start and thinking_done

	// Ctrl+C bookkeeping. Mirrors acorn/app.py:action_quit_check —
	// while generating: first Ctrl+C stops the run; while idle: first
	// Ctrl+C arms a "press again to quit" hint, second Ctrl+C within
	// 1s actually exits.
	lastCtrlC time.Time

	// thinkingBuf accumulates chat:thinking text chunks for the
	// current turn. On thinking_done we dump the last ~30 lines
	// into the chat log as a 💭 'thinking' system entry — matches
	// acorn/handlers/ws_events.py:on_status.
	thinkingBuf string

	// Output log — captured tool stdout/stderr lines for the current
	// session. Toggled with Ctrl+O. Bounded to ~500 entries.
	outputLog     []string
	outputLogOpen bool
	outputLogVP   viewport.Model
	outputLogInit bool
}

// SetProgram stores the reference so off-thread code can deliver messages.
// main.go calls this after tea.NewProgram returns.
func (m *Model) SetProgram(p *tea.Program) {
	m.sendProgramMsg = func(msg tea.Msg) { p.Send(msg) }
}

// New constructs the initial model.
//
// isContinue is set by `acorn -c` / `/resume`. When true, New loads the
// local JSONL session history and seeds m.messages before the first
// render, so the user sees prior turns instantly even if the server
// doesn't know the session (fresh SPORE, different machine, etc.).
// Matches acorn/app.py:_render_local_history.
func New(cfg *config.Config, cwd, sess string, planMode, isContinue bool) *Model {
	ta := textarea.New()
	ta.Placeholder = "type a message · /help for commands · Shift+Tab toggles plan mode"
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()

	vp := viewport.New(0, 0)
	vp.SetContent("")

	m := &Model{
		cfg:      cfg,
		cwd:      cwd,
		sess:     sess,
		input:    ta,
		viewport: vp,
		planMode: planMode,
		status:   "connecting…",
		theme:    themeForName(cfg.Display.Theme),
	}
	m.perms = newTUIPerms(m)
	m.exec = tools.New(m.perms, cwd, filepath.Join(cwd, ".acorn", "logs"))
	m.cmdHistory = loadHistory(cfg.GlobalDir)
	m.histIdx = -1
	m.followBottom = true
	m.currentStreamIdx = -1
	if w, err := sessionlog.Open(cfg.GlobalDir, sess); err == nil {
		m.writer = w
	}
	m.dlog = sessionlog.OpenDebug(cfg.GlobalDir, sess, cfg.Connection.User, cwd)
	m.exec.Hooks.OnExecLine = func(line string) {
		// Keep a low-noise preview in the status bar.
		preview := line
		if len(preview) > 120 {
			preview = preview[:120] + "…"
		}
		m.status = "⚙ " + preview
		// Append the full line to the output log buffer (Ctrl+O panel).
		m.outputLog = append(m.outputLog, line)
		if len(m.outputLog) > 500 {
			m.outputLog = m.outputLog[len(m.outputLog)-500:]
		}
	}
	m.exec.Hooks.OnToolDone = func(name string, input map[string]any, result any, ms int) {
		if m.writer != nil {
			m.writer.WriteTool(name, input, result, true, ms)
		}
		if m.dlog != nil {
			m.dlog.Info("tool", name, "ms", ms)
		}
	}
	m.exec.Hooks.OnCodeView = func(path, content string, isNew bool) {
		m.pushCodeView(path, content, isNew)
	}
	m.exec.Hooks.OnCodeDiff = func(path, oldT, newT string) {
		m.pushCodeDiff(path, oldT, newT)
	}
	if isContinue {
		m.loadLocalHistory()
		// Force context re-send on first message of the resumed session —
		// the remote agent's context may have aged out. Same rationale as
		// Python's ctx_manager.reset() call in _render_local_history.
		m.contextSent = false
	}
	return m
}

// loadLocalHistory seeds m.messages from the per-session JSONL file.
// Skips tool rows (too verbose for replay) and trailing empty entries.
// Sets historyDirty so the cache rebuilds on next render.
func (m *Model) loadLocalHistory() {
	entries := sessionlog.LoadSession(m.cfg.GlobalDir, m.sess)
	if len(entries) == 0 {
		return
	}
	userN, asstN := 0, 0
	for _, e := range entries {
		text := e.Text
		if text == "" {
			continue
		}
		role := e.Role
		switch role {
		case "user":
			userN++
		case "assistant":
			asstN++
		case "tool":
			continue
		default:
			role = "system"
		}
		m.messages = append(m.messages, chatMsg{Role: role, Text: text, Timestamp: time.Unix(int64(e.TS), 0)})
	}
	m.messages = append(m.messages, chatMsg{
		Role: "system",
		Text: fmt.Sprintf("── Local history replayed (%d sent, %d received) — context will be re-sent on next message ──", userN, asstN),
		Timestamp: time.Now(),
	})
	m.historyDirty = true
}

// historyKey is a cheap dedupe key so the server's chat:history reply
// doesn't add duplicates on top of the locally-loaded history.
func historyKey(role, text string) string {
	if len(text) > 120 {
		text = text[:120]
	}
	return role + "|" + text
}

func (m *Model) Init() tea.Cmd {
	m.client = conn.New(m.cfg.Connection.Host, m.cfg.Connection.Port, m.cfg.Connection.User, m.cfg.Connection.Key)
	m.client.OnConnected = func() {}
	m.client.OnDisconnected = func() {}
	if m.dlog != nil {
		m.client.Logger = func(level, tag, msg string) {
			switch level {
			case "error":
				m.dlog.Error(tag, msg)
			case "warn":
				m.dlog.Warn(tag, msg)
			case "info":
				m.dlog.Info(tag, msg)
			default:
				m.dlog.Debug(tag, msg)
			}
		}
	}
	return tea.Batch(
		m.dialCmd(),
		m.recvCmd(),
		m.toolCmd(),
		textarea.Blink,
		sizePollCmd(),
	)
}

// dialCmd runs authenticate + connect off the main tea goroutine.
func (m *Model) dialCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := m.client.Authenticate(ctx); err != nil {
			return connErrorMsg{err: err.Error()}
		}
		if err := m.client.Connect(ctx); err != nil {
			return connErrorMsg{err: err.Error()}
		}
		return connOpenMsg{}
	}
}

// recvCmd reads a single WS frame.
func (m *Model) recvCmd() tea.Cmd {
	return func() tea.Msg {
		f, ok := <-m.client.In
		if !ok {
			return connClosedMsg{}
		}
		return wsFrameMsg{frame: f}
	}
}

// toolCmd reads a single tool:request frame and executes it.
func (m *Model) toolCmd() tea.Cmd {
	return func() tea.Msg {
		f, ok := <-m.client.ToolRequests
		if !ok {
			return nil
		}
		var req proto.ToolRequest
		if err := json.Unmarshal(f.Raw, &req); err != nil {
			return nil
		}
		// Ack immediately.
		_ = m.client.Send(map[string]any{"type": "tool:ack", "id": req.ID})

		result, _ := m.exec.Execute(req.Name, req.Input)
		// ALWAYS reply with tool:result — even for server-side tools. The
		// server treats a `null` result as "client declined, please run
		// this yourself." Without the reply, the server hangs waiting on
		// the request and the agent times out after a few minutes
		// (visible to the user as web_search / graph_query / etc.
		// stalling for ~3 minutes before failing). This matches
		// acorn/connection.py:_handle_tool_request which sends
		// tool:result with result=None when the local executor returns
		// None.
		_ = m.client.Send(map[string]any{
			"type":   "tool:result",
			"id":     req.ID,
			"result": result,
		})
		return toolHandledMsg{name: req.Name}
	}
}

// ── internal message types ────────────────────────────────────────────
type connOpenMsg struct{}
type connErrorMsg struct{ err string }
type connClosedMsg struct{}
type wsFrameMsg struct{ frame conn.Frame }
type toolHandledMsg struct{ name string }
type permDecisionMsg struct{ allowed bool }
type spinnerTickMsg struct{}
type sizePollMsg struct{}

// sizePollCmd schedules the next terminal-size sanity check. We do this
// because Bubble Tea v0.25's Windows build has NO SIGWINCH equivalent
// (signals_windows.go's listenForResize is a no-op) — without polling,
// resizing a Windows terminal mid-session never delivers a
// WindowSizeMsg and the layout stays stuck at the startup dimensions.
// Linux/macOS get SIGWINCH already, but the poll is cheap and guards
// against terminals that don't propagate it (web TTYs, some muxers).
func sizePollCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return sizePollMsg{} })
}

// spinnerFrames cycles a Braille animation while we're generating.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerTickCmd schedules another spinner tick. ~10 fps is plenty.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// ── message log helpers ──────────────────────────────────────────────
func (m *Model) pushChat(role, text string) {
	m.messages = append(m.messages, chatMsg{Role: role, Text: text, Timestamp: time.Now()})
	m.historyDirty = true
	// New top-level message — surface it. If the user was scrolled up
	// reading history we still snap to the bottom so they don't miss
	// new arrivals; that matches what the Python TUI does.
	m.followBottom = true
	m.rerenderViewport()
	if m.writer != nil {
		switch role {
		case "user":
			m.writer.WriteUser(text)
		case "assistant":
			m.writer.WriteAssistant(text, nil, 0)
		}
	}
}

// streamMsg returns a pointer to the live streaming chatMsg, or nil
// if no stream is open. ALWAYS resolve through this — never cache the
// pointer across any m.messages mutation, because append may
// reallocate the backing array.
func (m *Model) streamMsg() *chatMsg {
	if m.currentStreamIdx < 0 || m.currentStreamIdx >= len(m.messages) {
		return nil
	}
	return &m.messages[m.currentStreamIdx]
}

func (m *Model) startStream() {
	m.messages = append(m.messages, chatMsg{Role: "assistant", Text: "", Timestamp: time.Now(), Streaming: true})
	m.currentStreamIdx = len(m.messages) - 1
	m.historyDirty = true
}

func (m *Model) appendDelta(t string) {
	if m.currentStreamIdx < 0 {
		m.startStream()
	}
	// Re-resolve every call — the pointer would be stale if anything
	// else appended to m.messages since startStream.
	if msg := m.streamMsg(); msg != nil {
		msg.Text += t
	}
	m.rerenderViewport()
}

func (m *Model) endStream() {
	msg := m.streamMsg()
	if msg != nil {
		text := strings.TrimSpace(msg.Text)
		// Empty bubble (agent went straight to a tool with no text) —
		// drop the entry instead of leaving an empty bordered box in
		// the transcript. The tool indicator that follows is enough
		// to mark "the agent did something here."
		if text == "" {
			idx := m.currentStreamIdx
			m.messages = append(m.messages[:idx], m.messages[idx+1:]...)
		} else {
			msg.Streaming = false
			if m.writer != nil {
				m.writer.WriteAssistant(text, nil, 0)
			}
		}
	}
	m.currentStreamIdx = -1
	// History changed — the just-finished assistant message goes from
	// "streaming" to "done", which may render differently (no cursor).
	m.historyDirty = true
	m.rerenderViewport()
}

// Broadcast sends a typed message to observers (companion app) with
// sessionId auto-filled. Mirrors acorn/bridge.py:broadcast.
func (m *Model) Broadcast(msgType string, kv map[string]any) {
	if m.client == nil {
		return
	}
	payload := make(map[string]any, len(kv)+2)
	for k, v := range kv {
		payload[k] = v
	}
	payload["type"] = msgType
	if _, ok := payload["sessionId"]; !ok {
		payload["sessionId"] = m.sess
	}
	_ = m.client.Send(payload)
}

// sendChat wraps a chat message with session metadata.
//
// displayText is the user's clean message before context/plan-prefix
// is glued on. The server forwards displayText to observers (mobile
// app, second tab) so they see what the user actually typed instead
// of the full context-stuffed payload. Matches Python's
// chat_message(..., display_text=display_text).
func (m *Model) sendChat(content, displayText string) tea.Cmd {
	return func() tea.Msg {
		payload := map[string]any{
			"type":      "chat",
			"sessionId": m.sess,
			"content":   content,
			"userName":  m.cfg.Connection.User,
			"cwd":       m.cwd,
		}
		if displayText != "" && displayText != content {
			payload["displayText"] = displayText
		}
		if err := m.client.Send(payload); err != nil {
			return connErrorMsg{err: err.Error()}
		}
		return nil
	}
}

// dirTag extracts the last path component for a session label.
func dirTag(cwd string) string {
	cwd = strings.TrimRight(cwd, string(filepath.Separator))
	return filepath.Base(cwd)
}

func _fmtBytes(n int) string { return fmt.Sprintf("%d bytes", n) }
