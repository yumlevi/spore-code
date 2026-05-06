package app

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
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

// glamourRenderer returns a renderer for the given theme + word width.
// Falls back to nil on construction failure — callers must handle that.
// Evicts oldest entries when the cache grows past glamourCacheMax.
func glamourRenderer(t Theme, width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	key := fmt.Sprintf("%s|%s|%s|%d", t.Name, t.Fg, t.Accent, width)
	glamourMu.Lock()
	defer glamourMu.Unlock()
	if r, ok := glamourCache[key]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyleForTheme(t)),
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

func markdownStyleForTheme(t Theme) glamouransi.StyleConfig {
	return glamouransi.StyleConfig{
		Document: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{},
			Margin:         uintPtr(0),
		},
		BlockQuote: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{Color: colorPtr(t.Muted)},
			Indent:         uintPtr(1),
			IndentToken:    strPtr("│ "),
		},
		Paragraph: glamouransi.StyleBlock{},
		List: glamouransi.StyleList{
			StyleBlock:  glamouransi.StyleBlock{},
			LevelIndent: 4,
		},
		Heading: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{
				Color:       colorPtr(t.Banner),
				Bold:        boolPtr(true),
				BlockSuffix: "\n",
			},
		},
		H1:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "# "}},
		H2:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "## "}},
		H3:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "### "}},
		H4:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "#### "}},
		H5:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "##### "}},
		H6:   glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "###### "}},
		Text: glamouransi.StylePrimitive{Color: colorPtr(t.Fg)},
		Emph: glamouransi.StylePrimitive{
			Color:  colorPtr(t.Accent2),
			Italic: boolPtr(true),
		},
		Strong: glamouransi.StylePrimitive{
			Color: colorPtr(t.Banner),
			Bold:  boolPtr(true),
		},
		HorizontalRule: glamouransi.StylePrimitive{
			Color:  colorPtr(t.Separator),
			Format: "\n──────\n",
		},
		Item:        glamouransi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Task: glamouransi.StyleTask{
			Ticked:   "[✓] ",
			Unticked: "[ ] ",
		},
		Link: glamouransi.StylePrimitive{
			Color:     colorPtr(t.Info),
			Underline: boolPtr(true),
		},
		LinkText: glamouransi.StylePrimitive{Color: colorPtr(t.Info)},
		Code: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{
				Color:  colorPtr(t.Accent),
				Prefix: "`",
				Suffix: "`",
			},
		},
		CodeBlock: glamouransi.StyleCodeBlock{
			StyleBlock: glamouransi.StyleBlock{
				StylePrimitive: glamouransi.StylePrimitive{Color: colorPtr(t.Fg)},
				Margin:         uintPtr(1),
			},
		},
		Table: glamouransi.StyleTable{
			CenterSeparator: strPtr("│"),
			ColumnSeparator: strPtr("│"),
			RowSeparator:    strPtr("─"),
		},
		DefinitionDescription: glamouransi.StylePrimitive{BlockPrefix: "\n• "},
	}
}

func colorPtr(c lipgloss.Color) *string {
	s := string(c)
	return &s
}

func strPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func uintPtr(n uint) *uint {
	return &n
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
	r := glamourRenderer(t, width)
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

func truncateCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width-1, "") + "…"
}

func truncateLineCells(s string, width int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return truncateCells(s, width)
}

func fitRenderedBlock(s string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = truncateCells(line, width)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func fitRenderedBlockWithBackground(s string, width, height int, bg lipgloss.Color) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = paintLineBackground(truncateCells(line, width), width, bg)
	}
	for len(lines) < height {
		lines = append(lines, paintLineBackground("", width, bg))
	}
	return strings.Join(lines, "\n")
}

func paintLineBackground(line string, width int, bg lipgloss.Color) string {
	if width <= 0 {
		return line
	}
	open := backgroundOpen(bg)
	if open == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+open)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+open)
	padW := width - ansi.StringWidth(line)
	if padW < 0 {
		padW = 0
	}
	return open + line + strings.Repeat(" ", padW) + "\x1b[0m"
}

func backgroundOpen(bg lipgloss.Color) string {
	raw := string(bg)
	if strings.HasPrefix(raw, "#") && len(raw) == 7 {
		r, rErr := strconv.ParseUint(raw[1:3], 16, 8)
		g, gErr := strconv.ParseUint(raw[3:5], 16, 8)
		b, bErr := strconv.ParseUint(raw[5:7], 16, 8)
		if rErr == nil && gErr == nil && bErr == nil {
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
		}
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 255 {
		return fmt.Sprintf("\x1b[48;5;%dm", n)
	}
	sample := lipgloss.NewStyle().Background(bg).Render("x")
	idx := strings.Index(sample, "x")
	if idx <= 0 {
		return ""
	}
	return sample[:idx]
}

func foregroundOpen(fg lipgloss.Color) string {
	raw := string(fg)
	if strings.HasPrefix(raw, "#") && len(raw) == 7 {
		r, rErr := strconv.ParseUint(raw[1:3], 16, 8)
		g, gErr := strconv.ParseUint(raw[3:5], 16, 8)
		b, bErr := strconv.ParseUint(raw[5:7], 16, 8)
		if rErr == nil && gErr == nil && bErr == nil {
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
		}
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 255 {
		return fmt.Sprintf("\x1b[38;5;%dm", n)
	}
	sample := lipgloss.NewStyle().Foreground(fg).Render("x")
	idx := strings.Index(sample, "x")
	if idx <= 0 {
		return ""
	}
	return sample[:idx]
}

func clipRenderedLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n")
}

func clipLinesHead(lines []string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[:maxLines]
}

func clipLinesTail(lines []string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

// Module-level styles referenced by modal files (theme-agnostic defaults;
// modals draw full-screen overlays so only their accents vary by theme).
var (
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#1e2133"))

	accentStyle = lipgloss.NewStyle().Foreground(themeDark.Accent).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(themeDark.Muted).Faint(true)
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

	// All modals render INLINE — slot into the input bar position,
	// chat history stays visible above. Matches the Python acorn UX
	// and lets the user reference the agent's message / tool call
	// while picking an option. (Old behavior: full-screen overlays
	// for questions / plan / permissions — swallowed the chat.)

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

	maxInputH := m.height - headerH - footerH - 3
	if maxInputH < 3 {
		maxInputH = 3
	}

	// Input region — when no modal is up, the standard textarea bar.
	// When any modal (question / plan-approval / permission) is active,
	// the modal view takes the input row's place (full chat width,
	// bordered, sized to its content). Same vertical slot, no
	// full-screen takeover.
	var input string
	switch m.modal {
	case modalQuestion:
		input = m.question.view(m.width, maxInputH, m.input.Value(), m.theme)
	case modalPlan:
		input = m.planApproval.view(m.width, maxInputH, m.theme)
	case modalPermission:
		input = m.permission.view(m.width, maxInputH, m.theme)
	default:
		m.input.SetWidth(m.width - 2)
		inputBorderColor := m.theme.Border
		if m.planMode {
			inputBorderColor = m.theme.PlanLabelBg
		}
		if m.generating || m.thinking {
			inputBorderColor = m.theme.Accent
		}
		inputBorder := borderStyle.Copy().
			BorderForeground(inputBorderColor).
			Foreground(m.theme.Fg).
			Width(m.width - 2)
		input = inputBorder.Render(m.input.View())
	}
	inputH := lipgloss.Height(input)
	if inputH > maxInputH {
		input = clipRenderedLines(input, maxInputH)
		inputH = lipgloss.Height(input)
	}

	// Provisional leftW for suggest (actual chat width may differ when
	// side panels show, but the dropdown floats over the chat column).
	provLeftW := m.width
	if side := m.renderSidePanels(); side != "" {
		provLeftW = m.width - lipgloss.Width(side) - 1
		if provLeftW < 40 {
			provLeftW = m.width
		}
	}
	suggest := ""
	if m.modal == modalNone {
		suggest = m.renderSuggest(provLeftW)
	}
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
	body = fitRenderedBlock(body, m.width, bodyH)

	parts := []string{header, body}
	if suggest != "" {
		parts = append(parts, suggest)
	}
	parts = append(parts, input, footer)
	return fitRenderedBlockWithBackground(lipgloss.JoinVertical(lipgloss.Left, parts...), m.width, m.height, m.theme.Bg)
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
	if len(m.codeViews) == 0 && (m.subagents == nil || len(m.subagents.Order) == 0) && (m.planTasks == nil || len(m.planTasks.Order) == 0) {
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
	showPlan := m.planTasks != nil && len(m.planTasks.Order) > 0
	if !showCode && !showSub && !showPlan {
		return ""
	}
	type panelSpec struct {
		render func(int) string
	}
	var active []panelSpec
	if showPlan {
		active = append(active, panelSpec{render: func(h int) string { return m.renderPlanTasksPanel(cw, h) }})
	}
	if showSub {
		active = append(active, panelSpec{render: func(h int) string { return m.renderSubagentPanel(cw, h) }})
	}
	if showCode {
		active = append(active, panelSpec{render: func(h int) string { return m.renderCodePanel(cw, h) }})
	}
	const minPanelH = 5
	maxPanels := totalH / minPanelH
	if maxPanels <= 0 {
		return ""
	}
	if maxPanels < len(active) {
		active = active[:maxPanels]
	}
	n := len(active)
	each := totalH / n
	extra := totalH - each*n
	heightFor := func() int {
		h := each
		if extra > 0 {
			h++
			extra--
		}
		return h
	}
	var panels []string
	for _, spec := range active {
		if p := spec.render(heightFor()); p != "" {
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
		Foreground(m.theme.PlanLabelFg).Bold(true).
		Background(m.theme.Accent).
		Padding(0, 1).Render("spore code " + Version)

	user := lipgloss.NewStyle().
		Foreground(m.theme.PromptUser).Background(m.theme.BgPanel).Bold(true).
		Padding(0, 1).
		Render(connIcon + " " + m.cfg.Connection.User)

	// project name + git branch — Python's prompt_user / prompt_project /
	// prompt_branch trio. Project is the cwd basename.
	proj := lipgloss.NewStyle().
		Foreground(m.theme.PromptProject).Background(hdrBg).Bold(true).
		Padding(0, 1).
		Render(dirTag(m.cwd))
	branch := ""
	if br := m.gitBranch; br != "" {
		branch = lipgloss.NewStyle().
			Foreground(m.theme.PromptBranch).Background(hdrBg).
			Padding(0, 1).
			Render("git:" + br)
	}

	sess := lipgloss.NewStyle().
		Foreground(m.theme.Muted).Background(hdrBg).Faint(true).
		Padding(0, 1).
		Render(short(m.sess))

	agentBadge := ""
	if name := cleanAgentName(m.agentName); name != "" {
		label := truncateLineCells(name, 24)
		agentBadge = lipgloss.NewStyle().
			Foreground(m.theme.Accent).Background(hdrBg).Bold(true).
			Padding(0, 1).
			Render(label)
	}

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
			Foreground(m.theme.Muted).Background(m.theme.BgPanel).
			Padding(0, 1).
			Render("perm:" + mp)
	}

	// Compose left + right, dropping optional pieces from the middle if
	// the terminal is too narrow to fit everything. Order of importance:
	// logo+user (always) > mode bar > perm badge > agent badge > activity > sess > project/branch.
	leftPieces := []string{logoBox, user, proj, branch, sess}
	rightPieces := []string{activity, agentBadge, permBadge, modeBar}
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
			rightPieces[1] = "" // drop agent badge
		case len(rightPieces) > 2 && rightPieces[2] != "":
			rightPieces[2] = "" // drop perm badge
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
	active := m.generating || m.thinking
	if active {
		status = m.activeFooterStatus()
	} else if status == "" {
		status = "enter send · alt+enter newline · shift+tab mode · pgup/pgdn or ctrl+↑/↓ scroll · ctrl+p panels · ctrl+o output · ctrl+c quit"
	}
	if !active {
		if wf := m.workflowLabel(); wf != "" {
			status = wf + " · " + status
		}
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
	fg := m.theme.Muted
	border := m.theme.Separator
	if active {
		fg = m.theme.Fg
		border = m.theme.Accent
	}
	return lipgloss.NewStyle().
		Foreground(fg).
		Background(m.theme.BgPanel).
		Padding(0, 1).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(border).
		Width(m.width).
		MaxWidth(m.width).
		Render(status)
}

func (m *Model) activeFooterStatus() string {
	spin := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	elapsed := time.Duration(0)
	if !m.activeSince.IsZero() {
		elapsed = time.Since(m.activeSince)
	}
	verb := "Working"
	if m.thinking {
		verb = "Thinking"
	}
	actor := truncateLineCells(m.agentDisplayName(), 24)
	parts := []string{fmt.Sprintf("%s %s %s %s", spin, actor, strings.ToLower(verb), formatWorkingElapsed(elapsed))}
	if wf := m.workflowLabel(); wf != "" {
		parts = append(parts, wf)
	}
	if detail := activeStatusDetail(m.status, m.thinking); detail != "" {
		parts = append(parts, detail)
	}
	if m.thinking && m.thinkingTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d thinking tokens", m.thinkingTokens))
	}
	parts = append(parts, "Ctrl+C to stop")
	return activeTextWave(strings.Join(parts, " · "), m.theme, m.spinnerFrame, m.thinking)
}

func foregroundSpan(text string, fg, base lipgloss.Color) string {
	open := foregroundOpen(fg)
	close := foregroundOpen(base)
	if open == "" || close == "" {
		return text
	}
	return open + text + close
}

func activeTextWave(text string, t Theme, frame int, thinking bool) string {
	width := lipgloss.Width(text)
	if width <= 0 {
		return text
	}
	var b strings.Builder
	pos := 0
	for _, r := range text {
		ch := string(r)
		w := ansi.StringWidth(ch)
		if w <= 0 {
			b.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			b.WriteRune(r)
			pos += w
			continue
		}
		color := activeWaveColor(t, frame, pos, width, thinking)
		if color == t.Fg {
			b.WriteRune(r)
		} else {
			b.WriteString(foregroundSpan(ch, color, t.Fg))
		}
		pos += w
	}
	return b.String()
}

func activeWaveColor(t Theme, frame, pos, width int, thinking bool) lipgloss.Color {
	accent := t.Accent
	if thinking {
		accent = t.Thinking
	}
	ar, ag, ab, ok := parseHexRGB(accent)
	if !ok {
		return accent
	}
	br, bg, bb, ok := parseHexRGB(t.Fg)
	if !ok {
		return accent
	}
	const radius = 8
	period := width + radius
	if period <= 0 {
		period = 1
	}
	center := (frame * 2) % period
	dist := pos - center
	if dist < 0 {
		dist = -dist
	}
	if dist >= radius {
		return t.Fg
	}
	intensity := radius - dist
	mix := func(a, b int) int {
		return a + (b-a)*intensity/radius
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", mix(br, ar), mix(bg, ag), mix(bb, ab)))
}

func parseHexRGB(c lipgloss.Color) (int, int, int, bool) {
	raw := string(c)
	if !strings.HasPrefix(raw, "#") || len(raw) != 7 {
		return 0, 0, 0, false
	}
	r, rErr := strconv.ParseUint(raw[1:3], 16, 8)
	g, gErr := strconv.ParseUint(raw[3:5], 16, 8)
	b, bErr := strconv.ParseUint(raw[5:7], 16, 8)
	if rErr != nil || gErr != nil || bErr != nil {
		return 0, 0, 0, false
	}
	return int(r), int(g), int(b), true
}

func activeStatusDetail(status string, thinking bool) string {
	status = strings.TrimSpace(status)
	switch status {
	case "", "waiting…":
		return ""
	case "thinking…":
		if thinking {
			return ""
		}
	}
	return status
}

func formatWorkingElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
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
	widthChanged := m.historyWidth != m.viewport.Width
	if widthChanged {
		// Width changed (resize, side-panel toggle, suggest dropdown) —
		// invalidate the cache, every panel needs a re-wrap.
		m.historyDirty = true
		m.historyWidth = m.viewport.Width
	}
	// Fast-path: nothing has changed since the last rebuild. A live
	// stream sets viewportDirty on each delta (see appendDelta) and
	// clears it at the end of this function, so keystrokes / mouse
	// motion / spinner ticks between deltas hit this skip and the
	// terminal reuses the previously-rendered content. Cuts the
	// streaming-active redraw cost from one full SetContent per Msg
	// down to one per delta.
	if !widthChanged && !m.historyDirty && !m.viewportDirty {
		return
	}
	if m.historyDirty {
		m.renderedHistory = m.renderHistoryPrefix()
		m.historyDirty = false
	}

	var content string
	if msg := m.streamMsg(); msg != nil {
		// Render just the streaming message and append.
		tail := renderMessageWithAgent(*msg, m.viewport.Width, m.theme, m.agentDisplayName())
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
	m.viewportDirty = false
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
		b.WriteString(renderMessageWithAgent(msg, m.viewport.Width, m.theme, m.agentDisplayName()))
		_ = i
	}
	return b.String()
}

// renderMessage draws a single chat panel. Styled to match the Python Rich
// look: bordered box per message, role label in the top-left.
func renderMessage(c chatMsg, width int, t Theme) string {
	return renderMessageWithAgent(c, width, t, "")
}

func renderMessageWithAgent(c chatMsg, width int, t Theme, agentName string) string {
	// Suppress empty assistant bubbles. A finished assistant turn with
	// no visible text happens when the agent emitted only a QUESTIONS:
	// block (plan-mode ROUTER 1/2) — the marker is intercepted into
	// QuestionsBuf, the visible Text is empty, but the message is kept
	// in history so postStreamChecks can parse the buffer. Rendering it
	// would leave a tiny empty bordered box in the transcript after the
	// questions modal closes.
	if c.Role == "assistant" && !c.Streaming && strings.TrimSpace(c.Text) == "" {
		return ""
	}
	if strings.TrimSpace(c.Text) == strings.TrimSpace(LogoFull) {
		return renderLogoMessage(width, t)
	}
	if c.Role == "system" {
		innerW := width - 2
		if innerW < 4 {
			innerW = width
		}
		if innerW < 1 {
			innerW = 1
		}
		wrapW := innerW - 2
		if wrapW < 1 {
			wrapW = innerW
		}
		wrapped := wrapForPanel(c.Text, wrapW)
		lines := strings.Split(wrapped, "\n")
		for i, line := range lines {
			lines[i] = truncateCells("  "+line, innerW)
		}
		return lipgloss.NewStyle().
			Foreground(t.System).Italic(true).
			Render(strings.Join(lines, "\n"))
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
		label = cleanAgentName(agentName)
		if label == "" {
			label = "agent"
		}
		label = truncateLineCells(label, innerW/2)
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

func renderLogoMessage(width int, t Theme) string {
	innerW := width - 2
	if innerW < 1 {
		innerW = width
	}
	open := foregroundOpen(t.Accent)
	if open == "" {
		open = lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render("")
	}
	lines := strings.Split(strings.Trim(LogoFull, "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		lines[i] = open + "\x1b[1m" + truncateCells(line, innerW) + "\x1b[0m"
	}
	return "\n" + strings.Join(lines, "\n")
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
		lastSpaceCells := -1
		cut := 0 // byte index to cut at
		for i, r := range rem {
			rw := runeWidth(r)
			if cellW+rw > w {
				// We've hit the width budget. Prefer a word break if
				// we saw a space in the second half of this slice.
				if lastSpaceCells > w/2 {
					cut = lastSpace
				} else {
					cut = i
				}
				break
			}
			if r == ' ' {
				lastSpace = i
				lastSpaceCells = cellW
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
	if ansi.StringWidth(s) <= 40 {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth(string(r)) > 38 {
		r = r[1:]
	}
	return "…" + string(r)
}

// renderOutputLog draws a full-screen overlay of the captured tool
// stdout/stderr lines for this session. Toggled with Ctrl+O.
func (m *Model) renderOutputLog() string {
	firstRender := !m.outputLogInit
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
	if firstRender || m.outputLogFollow {
		m.outputLogVP.GotoBottom()
		m.outputLogFollow = true
	}

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
	return fitRenderedBlockWithBackground(lipgloss.JoinVertical(lipgloss.Left, header, body2, footer), m.width, m.height, m.theme.Bg)
}

// Unused helpers kept for potential future use.
var _ = fmt.Sprintf
