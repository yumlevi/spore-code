package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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

	switch m.modal {
	case modalQuestion:
		return m.question.view(m.width, m.height)
	case modalPlan:
		return m.planApproval.view(m.width, m.height)
	case modalPermission:
		return m.permission.view(m.width, m.height, m.theme)
	}

	header := m.renderHeader()

	// Body splits into chat on the left + optional side panels on the right.
	sidePanels := m.renderSidePanels()
	var body string
	if sidePanels != "" {
		leftW := m.width - lipgloss.Width(sidePanels) - 1
		if leftW < 40 {
			leftW = m.width
			sidePanels = ""
		}
		m.viewport.Width = leftW
		m.rerenderViewport()
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), sidePanels)
	} else {
		m.viewport.Width = m.width
		body = m.viewport.View()
	}

	// Input panel — boxed like Python's prompt_toolkit prompt.
	inputStyle := borderStyle.Copy().BorderForeground(m.theme.Separator).Width(m.width - 2)
	input := inputStyle.Render(m.input.View())
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, input, footer)
}

func (m *Model) renderSidePanels() string {
	cw := m.codePanelWidth()
	h := m.viewport.Height
	if h <= 0 {
		return ""
	}
	var panels []string
	if p := m.renderCodePanel(cw, h/2); p != "" {
		panels = append(panels, p)
	}
	if p := m.renderSubagentPanel(cw, h/2); p != "" {
		panels = append(panels, p)
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
	modeBg := m.theme.ModeBarExecBg
	if m.planMode {
		mode = "PLAN"
		modeBg = m.theme.ModeBarPlanBg
	}
	connIcon := "●"
	if !m.connected {
		connIcon = "○"
	}

	logoBox := lipgloss.NewStyle().
		Foreground(m.theme.Accent).Bold(true).
		Background(m.theme.BgPanel).
		Padding(0, 1).Render("🌰 acorn")

	user := lipgloss.NewStyle().
		Foreground(m.theme.Fg).Background(m.theme.BgPanel).
		Padding(0, 1).
		Render(connIcon + " " + m.cfg.Connection.User)

	sess := lipgloss.NewStyle().
		Foreground(m.theme.Muted).Background(m.theme.BgPanel).Faint(true).
		Padding(0, 1).
		Render(short(m.sess))

	modeBar := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("#ffffff")).
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

	left := logoBox + user + sess
	right := permBadge + modeBar
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 0 {
		pad = 0
	}
	fill := lipgloss.NewStyle().Background(m.theme.BgPanel).Render(strings.Repeat(" ", pad))
	return left + fill + right
}

func (m *Model) renderFooter() string {
	status := m.status
	if status == "" {
		status = "enter send · alt+enter newline · shift+tab mode · pgup/pgdn scroll · ctrl+c quit"
	}
	return lipgloss.NewStyle().
		Foreground(m.theme.Muted).
		Background(m.theme.BgPanel).
		Padding(0, 1).
		Width(m.width).
		Render(status)
}

func (m *Model) layout() {
	m.input.SetWidth(m.width - 2)
	inputH := m.input.Height() + 2
	m.viewport.Height = m.height - 1 - inputH - 1
	if m.viewport.Height < 3 {
		m.viewport.Height = 3
	}
	m.rerenderViewport()
}

func (m *Model) rerenderViewport() {
	var b strings.Builder
	for i, msg := range m.messages {
		b.WriteString(renderMessage(msg, m.viewport.Width, m.theme))
		if i < len(m.messages)-1 {
			b.WriteString("\n")
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
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

	content := wrapForPanel(c.Text, innerW)

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

// wrapForPanel performs a simple soft-wrap. lipgloss can't do grapheme-aware
// wrapping on its own (needs reflow), so we do a minimal split here.
func wrapForPanel(s string, w int) string {
	if w <= 0 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for len(line) > w {
			// Try to break on a word boundary.
			cut := w
			if sp := strings.LastIndex(line[:w], " "); sp > w/2 {
				cut = sp
			}
			out = append(out, line[:cut])
			line = strings.TrimLeft(line[cut:], " ")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func short(s string) string {
	if len(s) <= 40 {
		return s
	}
	return "…" + s[len(s)-38:]
}

// Unused helpers kept for potential future use.
var _ = fmt.Sprintf
