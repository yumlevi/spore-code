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
	currentStream *chatMsg
	viewport      viewport.Model
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
func New(cfg *config.Config, cwd, sess string, planMode bool) *Model {
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
	return m
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

		result, claimed := m.exec.Execute(req.Name, req.Input)
		if claimed {
			_ = m.client.Send(map[string]any{
				"type":   "tool:result",
				"id":     req.ID,
				"result": result,
			})
		}
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

func (m *Model) startStream() {
	m.messages = append(m.messages, chatMsg{Role: "assistant", Text: "", Timestamp: time.Now(), Streaming: true})
	m.currentStream = &m.messages[len(m.messages)-1]
	m.historyDirty = true
}

func (m *Model) appendDelta(t string) {
	if m.currentStream == nil {
		m.startStream()
	}
	m.currentStream.Text += t
	// Don't mark historyDirty — only the streaming tail needs re-render,
	// not the cached prefix. rerenderViewport now skips the expensive
	// full rebuild when only the tail changed.
	m.rerenderViewport()
}

func (m *Model) endStream() {
	if m.currentStream != nil {
		m.currentStream.Streaming = false
		text := m.currentStream.Text
		m.currentStream = nil
		if m.writer != nil && text != "" {
			m.writer.WriteAssistant(text, nil, 0)
		}
	}
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
func (m *Model) sendChat(content string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.Send(map[string]any{
			"type":      "chat",
			"sessionId": m.sess,
			"content":   content,
			"userName":  m.cfg.Connection.User,
			"cwd":       m.cwd,
		})
		if err != nil {
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
