package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// glamourCache memoizes one glamour renderer per (theme, width) pair.
// Capped with a cheap LRU so a long session with many resize events
// doesn't accumulate dozens of TermRenderer instances forever. Sync
// because View() can fire from a different goroutine than Update.
var (
	glamourMu    sync.Mutex
	glamourCache = map[string]*glamour.TermRenderer{}
	glamourOrder []string // insertion order for LRU eviction
)

const glamourCacheMax = 6

// glamourRenderer returns a renderer for the given style + word width.
// Falls back to nil on construction failure — callers must handle that.
// Evicts oldest entries when the cache grows past glamourCacheMax.
func glamourRenderer(style string, width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	key := fmt.Sprintf("%s|%d", style, width)
	glamourMu.Lock()
	defer glamourMu.Unlock()
	if r, ok := glamourCache[key]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		r, err = glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return nil
		}
	}
	glamourCache[key] = r
	glamourOrder = append(glamourOrder, key)
	// Evict oldest until we're under the cap.
	for len(glamourOrder) > glamourCacheMax {
		old := glamourOrder[0]
		glamourOrder = glamourOrder[1:]
		delete(glamourCache, old)
	}
	return r
}

// glamourStyleForTheme picks a glamour style name compatible with the
// theme's brightness. glamour ships "dark", "light", "notty", "auto" etc.
func glamourStyleForTheme(t Theme) string {
	switch t.Name {
	case "light", "arctic":
		return "light"
	}
	return "dark"
}

// renderMarkdown runs text through glamour and clamps each output line
// to `width` display cells. The clamp is belt-and-braces: glamour does
// wrap at the width we asked, but tables, link underlines, and bullet
// indentation can produce lines that measure a cell or two wider than
// requested — and when that output gets wrapped AGAIN inside the
// outer lipgloss Width(...) box the re-wrap lands at unpredictable
// positions and eats characters from the surrounding text. Hard-
// truncating here keeps every line ≤ width so the outer box never
// re-wraps.
//
// Returns the original text unchanged if glamour fails (so we never
// silently drop a message).
func renderMarkdown(text string, width int, t Theme) string {
	r := glamourRenderer(glamourStyleForTheme(t), width)
	if r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	out = strings.TrimRight(out, "\n")
	// Clamp each line to width display cells. ansi.Truncate is ANSI-
	// aware so it preserves color codes across the cut.
	if width > 0 {
		lines := strings.Split(out, "\n")
		for i, ln := range lines {
			if ansi.StringWidth(ln) > width {
				lines[i] = ansi.Truncate(ln, width, "")
			}
		}
		out = strings.Join(lines, "\n")
	}
	return out
}

// Module-level styles referenced by modal files (theme-agnostic defaults;
// modals draw full-screen overlays so only their accents vary by theme).
var (
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#1e2133"))

	accentStyle = lipgloss.NewStyle().Foreground(themeDark.Accent).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(themeDark.Muted).Faint(true)
	botStyle    = lipgloss.NewStyle().Foreground(themeDark.BotPanel)
)

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting up…"
	}

	if m.outputLogOpen {
		return m.renderOutputLog()
	}

	if m.panelExpand {
		return m.renderExpandedPanel()
	}

	switch m.modal {
	case modalQuestion:
		return m.question.view(m.width, m.height, m.input.Value())
	case modalPlan:
		return m.planApproval.view(m.width, m.height)
	case modalPermission:
		return m.permission.view(m.width, m.height, m.theme)
	}

	// ── Layout algorithm ─────────────────────────────────────────────
	// Rows, top to bottom:
	//   header    — 1 line
	//   body      — flex: fills whatever's left
	//   suggest   — variable: 0 when hidden, 2..N lines when visible
	//   input     — m.input.Height() + 2 (border)
	//   footer    — 1 line
	// Total must equal m.height exactly, or the terminal scrolls and chat
	// gets pushed off the top when suggest appears.
	//
	// Pre-compute all fixed+variable rows first, then shrink the body to
	// consume exactly the remainder.

	header := m.renderHeader()
	headerH := lipgloss.Height(header)

	footer := m.renderFooter()
	footerH := lipgloss.Height(footer)

	// Input width reserves room for border.
	m.input.SetWidth(m.width - 2)
	inputBorder := borderStyle.Copy().BorderForeground(m.theme.Separator).Width(m.width - 2)
	input := inputBorder.Render(m.input.View())
	inputH := lipgloss.Height(input)

	// Provisional leftW for suggest (actual chat width may differ when
	// side panels show, but the dropdown floats over the chat column).
	provLeftW := m.width
	if side := m.renderSidePanels(); side != "" {
		provLeftW = m.width - lipgloss.Width(side) - 1
		if provLeftW < 40 {
			provLeftW = m.width
		}
	}
	suggest := m.renderSuggest(provLeftW)
	suggestH := 0
	if suggest != "" {
		suggestH = lipgloss.Height(suggest)
	}

	bodyH := m.height - headerH - suggestH - inputH - footerH
	if bodyH < 3 {
		bodyH = 3
	}

	// Viewport height = body height. Side panels get the same height so
	// they align with the chat column and never overflow.
	m.viewport.Height = bodyH

	// Side panels, now sized against the final bodyH.
	sidePanels := ""
	if side := m.renderSidePanelsBounded(bodyH); side != "" {
		sidePanels = side
	}

	leftW := m.width
	if sidePanels != "" {
		leftW = m.width - lipgloss.Width(sidePanels) - 1
		if leftW < 40 {
			leftW = m.width
			sidePanels = ""
		}
	}
	// Reserve 1 column for the chat scrollbar.
	m.viewport.Width = leftW - 1
	if m.viewport.Width < 20 {
		m.viewport.Width = leftW
	}
	m.rerenderViewport()

	chatBar := scrollbar(&m.viewport, bodyH, m.theme)
	chatView := lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), chatBar)

	var body string
	if sidePanels != "" {
		body = lipgloss.JoinHorizontal(lipgloss.Top, chatView, sidePanels)
	} else {
		body = chatView
	}

	parts := []string{header, body}
	if suggest != "" {
		parts = append(parts, suggest)
	}
	parts = append(parts, input, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderSidePanels — kept for the layout pre-pass that needs to know whether
// any side panel exists. Returns a single-line stub so the column-width
// calc treats them as present without rendering at full height yet.
func (m *Model) renderSidePanels() string {
	if m.panelHidden {
		return ""
	}
	if m.codePanelWidth() == 0 {
		return ""
	}
	if len(m.codeViews) == 0 && (m.subagents == nil || len(m.subagents.Order) == 0) {
		return ""
	}
	// Stub width-only; real render happens in renderSidePanelsBounded.
	return strings.Repeat(" ", m.codePanelWidth())
}

// renderSidePanelsBounded does the actual sized render once the body
// height is known. Panels fill the chat column's full height: a single
// active panel takes all of totalH; two panels split it evenly (with
// the bottom one absorbing any odd remainder so totals match exactly).
func (m *Model) renderSidePanelsBounded(totalH int) string {
	if m.panelHidden {
		return ""
	}
	cw := m.codePanelWidth()
	if totalH <= 0 || cw == 0 {
		return ""
	}
	showCode := len(m.codeViews) > 0
	showSub := m.subagents != nil && len(m.subagents.Order) > 0
	if !showCode && !showSub {
		return ""
	}
	codeH, subH := totalH, totalH
	if showCode && showSub {
		codeH = totalH / 2
		subH = totalH - codeH
	}
	var panels []string
	if showCode {
		if p := m.renderCodePanel(cw, codeH); p != "" {
			panels = append(panels, p)
		}
	}
	if showSub {
		if p := m.renderSubagentPanel(cw, subH); p != "" {
			panels = append(panels, p)
		}
	}
	if len(panels) == 0 {
		return ""
	}
	return lipgloss.JoinVertical(lipgloss.Left, panels...)
}

func (m *Model) codePanelWidth() int {
	// 32% of the screen, capped so chat stays readable.
	w := m.width * 32 / 100
	if w > 50 {
		w = 50
	}
	if w < 28 {
		return 0
	}
	return w
}

func (m *Model) renderHeader() string {
	mode := "EXEC"
	modeBg := m.theme.ExecLabelBg
	modeFg := m.theme.ExecLabelFg
	if m.planMode {
		mode = "PLAN"
		modeBg = m.theme.PlanLabelBg
		modeFg = m.theme.PlanLabelFg
	}
	connIcon := "●"
	if !m.connected {
		connIcon = "○"
	}

	hdrBg := m.theme.BgHeader
	logoBox := lipgloss.NewStyle().
		Foreground(m.theme.Accent).Bold(true).
		Background(hdrBg).
		Padding(0, 1).Render("🌰 acorn " + Version)

	user := lipgloss.NewStyle().
		Foreground(m.theme.PromptUser).Background(hdrBg).Bold(true).
		Padding(0, 1).
		Render(connIcon + " " + m.cfg.Connection.User)

	// project name + git branch — Python's prompt_user / prompt_project /
	// prompt_branch trio. Project is the cwd basename.
	proj := lipgloss.NewStyle().
		Foreground(m.theme.PromptProject).Background(hdrBg).
		Padding(0, 1).
		Render(dirTag(m.cwd))
	branch := ""
	if br := gitBranch(m.cwd); br != "" {
		branch = lipgloss.NewStyle().
			Foreground(m.theme.PromptBranch).Background(hdrBg).
			Padding(0, 1).
			Render(" " + br)
	}

	sess := lipgloss.NewStyle().
		Foreground(m.theme.Muted).Background(hdrBg).Faint(true).
		Padding(0, 1).
		Render(short(m.sess))

	// Activity spinner + thinking token count, only while a turn is live.
	activity := ""
	if m.generating || m.thinking {
		spin := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		text := spin
		if m.thinking && m.thinkingTokens > 0 {
			text = fmt.Sprintf("%s thinking · %d tok", spin, m.thinkingTokens)
		} else if m.thinking {
			text = spin + " thinking"
		}
		activity = lipgloss.NewStyle().
			Foreground(m.theme.Thinking).Background(hdrBg).
			Padding(0, 1).
			Render(text)
	}

	modeBar := lipgloss.NewStyle().Bold(true).
		Foreground(modeFg).
		Background(modeBg).Padding(0, 1).
		Render(mode)

	permBadge := ""
	if m.perms != nil {
		mp := string(m.perms.Mode())
		permBadge = lipgloss.NewStyle().
			Foreground(m.theme.Muted).Background(hdrBg).
			Padding(0, 1).
			Render("perm:" + mp)
	}

	// Compose left + right, dropping optional pieces from the middle if
	// the terminal is too narrow to fit everything. Order of importance:
	// logo+user (always) > mode bar > perm badge > activity > sess > project/branch.
	leftPieces := []string{logoBox, user, proj, branch, sess}
	rightPieces := []string{activity, permBadge, modeBar}
	for {
		left := strings.Join(leftPieces, "")
		right := strings.Join(rightPieces, "")
		pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
		if pad >= 0 {
			fill := lipgloss.NewStyle().Background(hdrBg).Render(strings.Repeat(" ", pad))
			return left + fill + right
		}
		// Drop the next-most-expendable piece. Removing the trailing
		// branch/sess/project from left, then activity/perm from right.
		switch {
		case len(leftPieces) > 2 && leftPieces[len(leftPieces)-1] != "":
			leftPieces = leftPieces[:len(leftPieces)-1]
		case rightPieces[0] != "":
			rightPieces[0] = "" // drop activity
		case len(rightPieces) > 1 && rightPieces[1] != "":
			rightPieces[1] = "" // drop perm badge
		default:
			// Final fallback — hard truncate the joined string with ANSI-safe
			// trim. Better a clipped header than a wrapped one.
			joined := strings.Join(leftPieces, "") + strings.Join(rightPieces, "")
			return ansi.Truncate(joined, m.width, "")
		}
	}
}

func (m *Model) renderFooter() string {
	status := m.status
	if status == "" {
		status = "enter send · alt+enter newline · shift+tab mode · pgup/pgdn or ctrl+↑/↓ scroll · ctrl+p panels · ctrl+o output · ctrl+c quit"
	}
	// Truncate first so lipgloss.Width(...) below doesn't overflow when
	// the status string is wider than the terminal. Width() pads but
	// doesn't clip — without this, narrow terminals see the footer wrap.
	maxInner := m.width - 2 // account for the Padding(0,1)
	if maxInner < 1 {
		maxInner = 1
	}
	if lipgloss.Width(status) > maxInner {
		status = ansi.Truncate(status, maxInner, "…")
	}
	return lipgloss.NewStyle().
		Foreground(m.theme.Muted).
		Background(m.theme.BgPanel).
		Padding(0, 1).
		Width(m.width).
		MaxWidth(m.width).
		Render(status)
}

// layout is now a thin notify — the real layout runs in View() each
// frame because heights depend on suggest visibility and side-panel
// presence which can change without a window resize.
func (m *Model) layout() {
	m.input.SetWidth(m.width - 2)
	m.rerenderViewport()
}

// rerenderViewport composes the chat content for the viewport. It uses a
// two-tier cache:
//
//   - renderedHistory: the rendered string for every COMPLETED message
//     (everything except the currently-streaming assistant turn). This is
//     cheap to keep around and is only rebuilt when historyDirty=true
//     (new message added, message finished streaming, role changed).
//
//   - The streaming tail is rendered fresh on every call. For long
//     conversations + fast deltas this drops the per-delta render cost
//     from O(N messages) to O(1).
func (m *Model) rerenderViewport() {
	if m.historyWidth != m.viewport.Width {
		// Width changed (resize, side-panel toggle, suggest dropdown) —
		// invalidate the cache, every panel needs a re-wrap.
		m.historyDirty = true
		m.historyWidth = m.viewport.Width
	}
	if m.historyDirty {
		m.renderedHistory = m.renderHistoryPrefix()
		m.historyDirty = false
	}

	var content string
	if msg := m.streamMsg(); msg != nil {
		// Render just the streaming message and append.
		tail := renderMessage(*msg, m.viewport.Width, m.theme)
		if m.renderedHistory == "" {
			content = tail
		} else {
			content = m.renderedHistory + "\n" + tail
		}
	} else {
		content = m.renderedHistory
	}
	prevYOffset := m.viewport.YOffset
	m.viewport.SetContent(content)
	if m.followBottom {
		m.viewport.GotoBottom()
	} else {
		// Preserve the user's scroll position. SetContent may have shifted
		// YOffset if the new content is shorter than the old; clamp it.
		if max := m.viewport.TotalLineCount() - m.viewport.Height; prevYOffset > max {
			m.viewport.YOffset = max
		} else {
			m.viewport.YOffset = prevYOffset
		}
	}
}

// renderHistoryPrefix renders every completed message — i.e. every
// message that isn't the currently-streaming one. Called only when
// historyDirty fires.
func (m *Model) renderHistoryPrefix() string {
	var b strings.Builder
	for i, msg := range m.messages {
		if msg.Streaming {
			// Skip the streaming entry — rerenderViewport appends it fresh.
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderMessage(msg, m.viewport.Width, m.theme))
		_ = i
	}
	return b.String()
}

// renderMessage draws a single chat panel. Styled to match the Python Rich
// look: bordered box per message, role label in the top-left.
func renderMessage(c chatMsg, width int, t Theme) string {
	if c.Role == "system" {
		return lipgloss.NewStyle().
			Foreground(t.System).Italic(true).
			Render("  " + c.Text)
	}

	innerW := width - 4
	if innerW < 20 {
		innerW = 20
	}

	var labelColor, borderColor, bodyColor lipgloss.Color
	var label string
	switch c.Role {
	case "user":
		label = "you"
		labelColor = t.UserPanel
		borderColor = t.UserPanel
		bodyColor = t.Fg
	case "assistant":
		label = "agent"
		labelColor = t.Accent2
		borderColor = t.Accent2
		bodyColor = t.BotPanel
	default:
		label = c.Role
		labelColor = t.Muted
		borderColor = t.Separator
		bodyColor = t.Muted
	}

	head := lipgloss.NewStyle().
		Bold(true).Foreground(labelColor).
		Render(label)

	trail := ""
	if c.Streaming {
		trail = lipgloss.NewStyle().Foreground(t.Accent).Blink(true).Render("▌")
	}

	var content string
	if c.Role == "assistant" && !c.Streaming && c.Text != "" {
		content = renderMarkdown(c.Text, innerW, t)
	} else {
		content = wrapForPanel(c.Text, innerW)
	}

	header := head
	if trail != "" {
		header = head + " " + trail
	}

	// Border style + padding = Python's bordered Panel look.
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Foreground(bodyColor).
		Padding(0, 1).
		Width(width - 2).
		Render(header + "\n" + content)
	return box
}

// wrapForPanel soft-wraps each line to at most w display cells wide,
// preferring word boundaries. RUNE-AWARE — cuts never land inside a
// multi-byte UTF-8 sequence, which the previous byte-slicing version
// was silently corrupting. That showed up as 'chars dropping mid-word'
// in streamed agent output once the message contained em-dashes,
// smart quotes, bullets, or box-drawing characters (think QR-code
// responses).
//
// Uses ansi.StringWidth for cell counting so wide CJK / emoji behave
// correctly. ANSI escape sequences, if any slipped in, are counted as
// zero-width (which is correct — they don't take visual cells).
func wrapForPanel(s string, w int) string {
	if w <= 0 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapLine(line, w)...)
	}
	return strings.Join(out, "\n")
}

// wrapLine wraps a single logical line (no newlines in `s`) to at most
// w display cells per wrapped row. Splits on spaces where possible;
// hard-splits long runs. Returns at least one entry (possibly empty).
func wrapLine(s string, w int) []string {
	if s == "" {
		return []string{""}
	}
	if ansi.StringWidth(s) <= w {
		return []string{s}
	}
	var out []string
	rem := s
	for ansi.StringWidth(rem) > w {
		// Scan rune-by-rune accumulating display width; remember the
		// last space position to prefer a word break.
		cellW := 0
		lastSpace := -1
		cut := 0 // byte index to cut at
		for i, r := range rem {
			rw := runeWidth(r)
			if cellW+rw > w {
				// We've hit the width budget. Prefer a word break if
				// we saw a space in the second half of this slice.
				if lastSpace > w/2 {
					cut = lastSpace
				} else {
					cut = i
				}
				break
			}
			if r == ' ' {
				lastSpace = i
			}
			cellW += rw
			cut = i + utf8Len(r) // end-of-string fallback
		}
		if cut <= 0 {
			cut = len(rem)
		}
		out = append(out, rem[:cut])
		// Trim leading spaces on the continuation line so wrapped prose
		// doesn't start with an awkward gap.
		rem = strings.TrimLeft(rem[cut:], " ")
		if rem == "" {
			break
		}
	}
	if rem != "" {
		out = append(out, rem)
	}
	return out
}

// runeWidth returns the display-cell width of r. East-Asian wide + emoji
// = 2, control chars = 0, everything else = 1. Delegates to ansi so the
// logic matches lipgloss's width counter.
func runeWidth(r rune) int {
	return ansi.StringWidth(string(r))
}

// utf8Len returns the number of bytes in the UTF-8 encoding of r.
func utf8Len(r rune) int {
	switch {
	case r < 0x80:
		return 1
	case r < 0x800:
		return 2
	case r < 0x10000:
		return 3
	default:
		return 4
	}
}

func short(s string) string {
	if len(s) <= 40 {
		return s
	}
	return "…" + s[len(s)-38:]
}

// renderOutputLog draws a full-screen overlay of the captured tool
// stdout/stderr lines for this session. Toggled with Ctrl+O.
func (m *Model) renderOutputLog() string {
	if !m.outputLogInit {
		m.outputLogVP = viewport.New(m.width-2, m.height-3)
		m.outputLogInit = true
	}
	m.outputLogVP.Width = m.width - 2
	m.outputLogVP.Height = m.height - 3

	body := strings.Join(m.outputLog, "\n")
	if body == "" {
		body = lipgloss.NewStyle().Foreground(m.theme.Muted).Italic(true).
			Render("(no captured output yet — tool stdout/stderr will appear here)")
	}
	m.outputLogVP.SetContent(body)
	m.outputLogVP.GotoBottom()

	header := lipgloss.NewStyle().
		Foreground(m.theme.Banner).Bold(true).
		Background(m.theme.BgHeader).
		Padding(0, 1).Width(m.width).
		Render("📜 Output log — " + fmt.Sprintf("%d lines", len(m.outputLog)))
	footer := lipgloss.NewStyle().
		Foreground(m.theme.Muted).
		Background(m.theme.BgPanel).
		Padding(0, 1).Width(m.width).
		Render("ctrl+o close · ↑/↓ scroll · g/G top/bottom")

	scroll := scrollbar(&m.outputLogVP, m.outputLogVP.Height, m.theme)
	body2 := lipgloss.JoinHorizontal(lipgloss.Top, m.outputLogVP.View(), scroll)
	return lipgloss.JoinVertical(lipgloss.Left, header, body2, footer)
}

// Unused helpers kept for potential future use.
var _ = fmt.Sprintf
