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

	"github.com/yumlevi/spore-code/internal/config"
	"github.com/yumlevi/spore-code/internal/conn"
	"github.com/yumlevi/spore-code/internal/proto"
	"github.com/yumlevi/spore-code/internal/sessionlog"
	"github.com/yumlevi/spore-code/internal/tools"
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

	// Questions-block streaming state. When the assistant emits
	// `\nQUESTIONS:` in plan mode, appendDelta flips InQuestionsBlock
	// and routes subsequent deltas into QuestionsBuf instead of Text
	// so the raw JSON never appears in the visible chat. On
	// chat:done the buffer is parsed into the questions modal; on
	// parse failure it's flushed back into Text as a safety net so
	// nothing is silently eaten.
	InQuestionsBlock bool
	QuestionsBuf     string
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
	contextSent  bool // gather_context only on first message — only used in legacy fallback path
	// scope governs the sandbox for local file ops. "" / "strict"
	// (default) enforces cwd-only access in tools/fileops.go and
	// tells the agent so via the system prompt. "expanded" turns
	// both off. Toggled with /scope.
	scope string
	generating   bool

	// serverCaps is what SPORE told us it supports on connect. We send
	// projectContext as a sibling field on chat:submit when this is set,
	// otherwise we fall back to gluing GatherContext onto the message
	// content for backwards compatibility with old SPORE builds.
	serverCaps proto.ServerCapabilities
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

	// questionsRetryAttempted tracks whether we've already auto-asked
	// the agent to re-emit a malformed QUESTIONS: block this turn.
	// Reset to false the moment a clean parse succeeds OR we've used
	// our one retry. Prevents infinite loops if the agent keeps
	// emitting broken JSON.
	questionsRetryAttempted bool

	// sendProgramMsg is wired by main.go after tea.NewProgram so off-thread
	// goroutines (the permissions blocking prompt) can poke the UI.
	sendProgramMsg func(msg tea.Msg)

	// Session writer — appends every user/assistant/tool turn to a JSONL
	// file under ~/.spore-code/sessions/ for crash recovery + /resume picker.
	writer *sessionlog.Writer

	// Diagnostic log at ~/.spore-code/logs/<ts>_<session>.log — matches Python's
	// session_log.py output for parity across acorn variants.
	dlog *sessionlog.DebugLogger

	// Side panel state for code viewer + subagent activity.
	codeViews    []codeViewEntry
	subagents    *subagentPanel
	planTasks    *planTaskPanel     // plan-mode execution checklist (task:* frames)
	panelExpand  bool               // ctrl+p opens a full-height browser
	panelView    viewport.Model     // scrollable viewport for the expanded panel
	panelViewInit bool
	panelHidden  bool               // /panel hide — suppress the right-column activity panel entirely

	// Slash-command autocomplete state.
	suggest slashSuggest

	// Streaming optimization. renderedHistory caches the lipgloss output
	// for every message EXCEPT the one currently streaming, so chat:delta
	// updates only re-render the tail. tailDirty marks the current
	// streaming block as needing a re-render on next View().
	renderedHistory string
	historyDirty    bool
	historyWidth    int // width the cache was rendered against
	// viewportDirty: set whenever m.messages or stream tail changes. View()
	// only calls rerenderViewport when set (or width changed), then clears
	// it. Without this flag, every keystroke/mouse-motion/tick rebuilt the
	// full viewport content (lipgloss render of the streaming tail +
	// SetContent line-split of the whole transcript), blocking the input
	// loop. ~10× fewer rebuilds per second during a stream.
	viewportDirty bool
	// questionsScanOff: how far into msg.Text we've already scanned for
	// the QUESTIONS: marker. Avoids re-scanning the entire accumulated
	// streaming text on each delta (was O(N²) total).
	questionsScanOff int

	// followBottom — when true, every render snaps the viewport to the
	// last line so streaming text stays visible. Set to false the moment
	// the user scrolls up so they can read history without it being
	// yanked back. Set true again when they scroll all the way down.
	followBottom bool

	// Command history — Up/Down in input cycles through prior sends.
	// Persisted to ~/.spore-code/history (plain text, one entry per line) so
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
	// Defaults from bubbles/textarea cap each line at 500 columns and
	// the buffer at 99 rows. Both are wrong for an agent prompt — pasting
	// a long URL (oauth callbacks, GitHub raw links with tokens) or a
	// stack trace easily blows past 500 cols, and a multi-paragraph
	// message blows past 99 rows. CharLimit is already 0 (unlimited)
	// by default; set MaxWidth and MaxHeight to 0 to remove the per-line
	// and total-rows caps too.
	ta.CharLimit = 0
	ta.MaxWidth = 0
	ta.MaxHeight = 0
	// We intercept plain 'enter' in updateKey to send the message, which
	// means textarea never sees it. To keep the multi-line-draft path
	// working we remap InsertNewline to 'alt+enter' (the binding
	// we advertise in the placeholder + footer). 'ctrl+j' kept as a
	// backup in case the terminal eats alt-combos — some Windows
	// consoles swallow Alt meta keys.
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
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
	m.exec = tools.New(m.perms, cwd, filepath.Join(cwd, ".spore-code", "logs"))
	m.cmdHistory = loadHistory(cfg.GlobalDir)
	m.histIdx = -1
	m.followBottom = true
	m.currentStreamIdx = -1
	if w, err := sessionlog.Open(cfg.GlobalDir, sess); err == nil {
		m.writer = w
	}
	m.dlog = sessionlog.OpenDebug(cfg.GlobalDir, sess, cfg.Connection.User, cwd)
	// All four hooks fire from the tool-executor goroutine. Route
	// every one through sendProgramMsg so the actual Model mutation
	// happens on the main Update goroutine — concurrent slice appends
	// were the real cause of the scrambled chat-bubble text seen on
	// long multi-tool turns.
	m.exec.Hooks.OnExecLine = func(line string) {
		if m.sendProgramMsg != nil {
			m.sendProgramMsg(hookExecLineMsg{line: line})
		}
	}
	m.exec.Hooks.OnToolDone = func(name string, input map[string]any, result any, ms int) {
		if m.sendProgramMsg != nil {
			m.sendProgramMsg(hookToolDoneMsg{name: name, input: input, result: result, ms: ms})
		}
	}
	m.exec.Hooks.OnCodeView = func(path, content string, isNew bool) {
		if m.sendProgramMsg != nil {
			m.sendProgramMsg(hookCodeViewMsg{path: path, content: content, isNew: isNew})
		}
	}
	m.exec.Hooks.OnCodeDiff = func(path, oldT, newT string) {
		if m.sendProgramMsg != nil {
			m.sendProgramMsg(hookCodeDiffMsg{path: path, oldT: oldT, newT: newT})
		}
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
		// Background ping to GitHub Releases. Silent if up-to-date or
		// the network is unreachable; only surfaces when a newer tag
		// exists. See bootCheckUpdateCmd in updater.go.
		bootCheckUpdateCmd(),
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
type spinnerTickMsg struct{}
type sizePollMsg struct{}

// hookMsg variants — fired from the tool-executor goroutine via
// sendProgramMsg so Model mutations stay on the main Update thread.
// Without this, concurrent appends to m.messages (chat:delta on main)
// and m.codeViews (OnCodeView from the tool goroutine) raced — the
// visible symptom was scrambled / chunk-dropped chat bubble text on
// long agent turns.
type hookExecLineMsg struct{ line string }
type hookCodeViewMsg struct {
	path    string
	content string
	isNew   bool
}
type hookCodeDiffMsg struct{ path, oldT, newT string }
type hookToolDoneMsg struct {
	name   string
	input  map[string]any
	result any
	ms     int
}

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
	m.viewportDirty = true
	m.questionsScanOff = 0
}

func (m *Model) appendDelta(t string) {
	if m.currentStreamIdx < 0 {
		m.startStream()
	}
	// Re-resolve every call — the pointer would be stale if anything
	// else appended to m.messages since startStream.
	msg := m.streamMsg()
	if msg == nil {
		return
	}
	// Plan-mode questions intercept: once the rolling text contains a
	// newline-anchored "QUESTIONS:" marker, everything from that
	// point on goes into QuestionsBuf instead of visible Text. The
	// JSON body is handed to the questions parser on chat:done; on
	// parse failure the buffer is flushed back into Text so a
	// malformed emission isn't lost silently.
	if msg.InQuestionsBlock {
		msg.QuestionsBuf += t
		m.viewportDirty = true
		return
	}
	// Only scan the new tail (with a small overlap to catch a marker
	// that straddles two deltas — len("QUESTIONS:")=10 bytes is enough).
	// Was O(N) per delta over the full accumulated text → quadratic over
	// the stream. Now O(len(t) + 10).
	scanStart := m.questionsScanOff
	if scanStart > len(msg.Text) {
		scanStart = 0
	}
	if scanStart > 10 {
		scanStart -= 10
	} else {
		scanStart = 0
	}
	combined := msg.Text + t
	if idx := findQuestionsMarkerFrom(combined, scanStart); idx >= 0 {
		msg.Text = strings.TrimRight(combined[:idx], " \t")
		msg.Text = strings.TrimRight(msg.Text, "\n")
		msg.QuestionsBuf = combined[idx:]
		msg.InQuestionsBlock = true
		m.questionsScanOff = 0
	} else {
		msg.Text += t
		m.questionsScanOff = len(msg.Text)
	}
	m.viewportDirty = true
}

// findQuestionsMarkerFrom scans s for the left-anchored QUESTIONS:
// marker plan mode uses to signal a questions block, starting at
// `start`. Returns the index of the 'Q' in "QUESTIONS:", or -1.
// The marker is recognized only when it appears at the start of a
// line (so prose references to the word don't fire). The `start`
// offset lets streaming callers re-scan only new bytes plus a small
// overlap window — avoids the O(N²) per-delta full re-scan.
func findQuestionsMarkerFrom(s string, start int) int {
	const marker = "QUESTIONS:"
	if start < 0 {
		start = 0
	}
	for {
		i := strings.Index(s[start:], marker)
		if i < 0 {
			return -1
		}
		abs := start + i
		if abs == 0 {
			return abs
		}
		j := abs - 1
		for j >= 0 && (s[j] == ' ' || s[j] == '\t') {
			j--
		}
		if j < 0 || s[j] == '\n' {
			return abs
		}
		start = abs + len(marker)
		if start >= len(s) {
			return -1
		}
	}
}

func (m *Model) endStream() {
	msg := m.streamMsg()
	if msg != nil {
		text := strings.TrimSpace(msg.Text)
		// Empty bubble (agent went straight to a tool with no text) —
		// drop the entry instead of leaving an empty bordered box in
		// the transcript. The tool indicator that follows is enough
		// to mark "the agent did something here."
		//
		// EXCEPTION: don't drop when QuestionsBuf is populated. The
		// plan-mode ROUTER prompts instruct the agent to emit ONLY a
		// QUESTIONS: block (no preamble) — appendDelta then redirects
		// the entire response into QuestionsBuf, leaving Text empty.
		// Dropping here would erase the buffer that postStreamChecks
		// needs to parse, so the questions modal never opens.
		if text == "" && msg.QuestionsBuf == "" {
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
	m.viewportDirty = true
	m.questionsScanOff = 0
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
//
// projectContext is the structured project metadata. Always sent when
// SPORE advertised the capability — server routes it into the system
// prompt so it never accumulates in messages[]. When the capability
// isn't advertised, callers should glue GatherContext() onto content
// instead and pass an empty ProjectContext here.
func (m *Model) sendChat(content, displayText string, projectCtx *proto.ProjectContext) tea.Cmd {
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
		if projectCtx != nil {
			payload["projectContext"] = projectCtx
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
