package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// codeViewEntry tracks a single file view/diff/thinking event in the
// chronological activity feed shown by the right-column panel.
type codeViewEntry struct {
	Path     string
	Content  string // full body (truncated) for code-view entries
	OldText  string
	NewText  string
	Text     string // short label like "203 lines, 4124 bytes" or "+12/-3 lines"
	Preview  string // a few-line peek shown under the header in the panel
	IsDiff   bool
	IsNew    bool
	Thinking bool // true when this is an accumulated agent-reasoning entry
	Tool     string
	ExecCmd  string
	ExecOut  []string
	When     time.Time
}

// pushCodeView records a read_file / write_file hit.
func (m *Model) pushCodeView(path, content string, isNew bool) {
	lineCount := strings.Count(content, "\n") + 1
	e := codeViewEntry{
		Path:    path,
		Content: truncateStr(content, 4000),
		Preview: previewLines(content, 4, 120),
		Text:    fmt.Sprintf("%d lines, %d bytes", lineCount, len(content)),
		IsNew:   isNew,
		When:    time.Now(),
	}
	m.appendActivity(e)
}

// pushCodeDiff records an edit_file hit.
func (m *Model) pushCodeDiff(path, oldT, newT string) {
	added := strings.Count(newT, "\n")
	removed := strings.Count(oldT, "\n")
	m.appendActivity(codeViewEntry{
		Path:    path,
		OldText: truncateStr(oldT, 2000),
		NewText: truncateStr(newT, 2000),
		Preview: diffPreview(oldT, newT, 4, 120),
		Text:    fmt.Sprintf("+%d / -%d lines", added, removed),
		IsDiff:  true,
		When:    time.Now(),
	})
}

// appendThinking accumulates a chat:thinking text chunk. Successive
// chunks within the same thinking block are merged into one entry so
// the panel doesn't churn one row per token.
func (m *Model) appendThinking(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if n := len(m.codeViews); n > 0 && m.codeViews[n-1].Thinking {
		last := &m.codeViews[n-1]
		last.Content = truncateStr(last.Content+text, 4000)
		last.Preview = previewLines(last.Content, 6, 120)
		last.Text = fmt.Sprintf("%d chars", len(last.Content))
		last.When = time.Now()
		return
	}
	m.appendActivity(codeViewEntry{
		Thinking: true,
		Content:  truncateStr(text, 4000),
		Preview:  previewLines(text, 6, 120),
		Text:     fmt.Sprintf("%d chars", len(text)),
		When:     time.Now(),
	})
}

// appendToolExec records a server-side tool execution (status frame
// arrives with name + detail) — gives the user a quick sense of what
// the agent is doing besides reads/writes.
func (m *Model) appendToolExec(tool, detail string) {
	m.appendActivity(codeViewEntry{
		Tool:    tool,
		ExecCmd: detail,
		Preview: trimTo(detail, 240),
		Text:    "tool",
		When:    time.Now(),
	})
}

func (m *Model) appendActivity(e codeViewEntry) {
	m.codeViews = append(m.codeViews, e)
	if len(m.codeViews) > 100 {
		m.codeViews = m.codeViews[len(m.codeViews)-100:]
	}
}

// previewLines returns up to maxLines from s, each clipped to maxW.
// Skips empty leading lines so previews don't waste rows on blank
// padding from the start of a file.
func previewLines(s string, maxLines, maxW int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, maxLines)
	for _, ln := range lines {
		if len(out) == 0 && strings.TrimSpace(ln) == "" {
			continue
		}
		if len(ln) > maxW {
			ln = ln[:maxW-1] + "…"
		}
		out = append(out, ln)
		if len(out) >= maxLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

// diffPreview shows a few lines from the new text — for an edit it's
// usually the just-written content. Stripped to the first non-blank
// run so the panel surfaces the actually-interesting hunk.
func diffPreview(oldT, newT string, maxLines, maxW int) string {
	if newT == "" {
		return previewLines(oldT, maxLines, maxW)
	}
	return previewLines(newT, maxLines, maxW)
}

// renderCodePanel returns the compact right-column activity panel:
// thinking bursts + file reads/writes/edits, newest at the bottom,
// each with a small content preview. Height is hard-capped by maxH;
// older entries scroll off the top.
func (m *Model) renderCodePanel(width, maxH int) string {
	if len(m.codeViews) == 0 || width < 20 || maxH < 5 {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render("Activity")
	bodyH := maxH - 4 // border(2) + padding(2)
	if bodyH < 1 {
		bodyH = 1
	}
	innerW := width - 4 // border + padding
	if innerW < 12 {
		innerW = 12
	}

	// Walk newest → oldest, accumulating rendered blocks until we've
	// used up bodyH. Each block: 1 header line + N preview lines.
	type block struct {
		lines []string
	}
	var blocks []block
	used := 0
	for i := len(m.codeViews) - 1; i >= 0; i-- {
		e := m.codeViews[i]
		header := renderActivityHeader(e, m.theme, innerW)
		preview := renderActivityPreview(e, m.theme, innerW)
		need := 1 + len(preview) + 1 // +1 blank separator between blocks
		if used+need > bodyH && len(blocks) > 0 {
			break
		}
		b := block{lines: append([]string{header}, preview...)}
		blocks = append(blocks, b)
		used += need
	}
	// Reverse so oldest of the visible window renders first.
	rendered := make([]string, 0, len(blocks))
	for i := len(blocks) - 1; i >= 0; i-- {
		if i != len(blocks)-1 {
			rendered = append(rendered, "") // blank separator
		}
		rendered = append(rendered, blocks[i].lines...)
	}
	hidden := len(m.codeViews) - len(blocks)
	more := ""
	if hidden > 0 {
		more = lipgloss.NewStyle().Foreground(m.theme.Muted).
			Render(fmt.Sprintf("  %d older hidden — Ctrl+P to expand", hidden))
	}
	inner := title
	if more != "" {
		inner += "  " + more
	}
	inner += "\n\n" + strings.Join(rendered, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent2).
		Padding(0, 1).
		Width(width - 2).
		Height(maxH - 2).
		Render(inner)
}

// renderActivityHeader is the bold/colored top line for one entry —
// icon, path/label, and meta.
func renderActivityHeader(e codeViewEntry, t Theme, innerW int) string {
	icon := "📄"
	color := t.ReadIcon
	label := e.Path
	switch {
	case e.Thinking:
		icon = "💭"
		color = t.Thinking
		label = "thinking"
	case e.Tool != "":
		icon = "⚙"
		color = t.ToolIcon
		label = e.Tool
	case e.IsDiff:
		icon = "✏"
		color = t.EditIcon
	case e.IsNew:
		icon = "🆕"
		color = t.DiffAdd
	}
	maxLabel := innerW - 4 - 10 // icon + space + meta tail
	if maxLabel < 6 {
		maxLabel = 6
	}
	if len(label) > maxLabel {
		label = "…" + label[len(label)-maxLabel+1:]
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(color).Render(icon + " " + label)
	meta := lipgloss.NewStyle().Foreground(t.Muted).Faint(true).
		Render(" · " + e.When.Format("15:04:05") + "  " + e.Text)
	return head + meta
}

// renderActivityPreview returns 0..N indented preview lines for an
// entry. Empty if the entry has nothing worth previewing.
func renderActivityPreview(e codeViewEntry, t Theme, innerW int) []string {
	if e.Preview == "" {
		return nil
	}
	style := lipgloss.NewStyle().Foreground(t.Muted)
	if e.Thinking {
		style = style.Italic(true)
	}
	indent := "   "
	maxW := innerW - len(indent)
	if maxW < 8 {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(e.Preview, "\n") {
		if len(ln) > maxW {
			ln = ln[:maxW-1] + "…"
		}
		out = append(out, style.Render(indent+ln))
	}
	return out
}

// subagent panel — tracks subagent:* ws frames.
type subagentPanel struct {
	Tasks map[string]*subagentState
	Order []string
}

type subagentState struct {
	TaskID  string
	Title   string
	Status  string
	Lines   []string
	Updated time.Time
}

func newSubagentPanel() *subagentPanel {
	return &subagentPanel{Tasks: map[string]*subagentState{}}
}

func (m *Model) handleSubagentFrame(verb string, raw map[string]any) {
	if m.subagents == nil {
		m.subagents = newSubagentPanel()
	}
	id := asString(raw["taskId"], "")
	if id == "" {
		id = asString(raw["id"], "")
	}
	if id == "" {
		return
	}
	st, ok := m.subagents.Tasks[id]
	if !ok {
		st = &subagentState{TaskID: id, Status: "running"}
		m.subagents.Tasks[id] = st
		m.subagents.Order = append(m.subagents.Order, id)
	}
	st.Updated = time.Now()
	switch verb {
	case "start":
		st.Title = asString(raw["task"], asString(raw["title"], ""))
	case "line", "log":
		line := asString(raw["text"], asString(raw["line"], ""))
		if line != "" {
			st.Lines = append(st.Lines, line)
			if len(st.Lines) > 50 {
				st.Lines = st.Lines[len(st.Lines)-50:]
			}
		}
	case "done":
		st.Status = "done"
	case "error":
		st.Status = "error"
	case "cancelled":
		st.Status = "cancelled"
	}
}

func (m *Model) renderSubagentPanel(width, maxH int) string {
	if m.subagents == nil || len(m.subagents.Order) == 0 || width < 20 || maxH < 5 {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render("Subagents")
	bodyH := maxH - 4
	if bodyH < 1 {
		bodyH = 1
	}
	// Each task: 1 title line + 1 status line = 2 lines. Fit from tail.
	perTask := 2
	maxTasks := bodyH / perTask
	if maxTasks < 1 {
		maxTasks = 1
	}
	start := len(m.subagents.Order) - maxTasks
	if start < 0 {
		start = 0
	}
	var lines []string
	for _, id := range m.subagents.Order[start:] {
		st := m.subagents.Tasks[id]
		icon := "●"
		color := m.theme.Accent
		switch st.Status {
		case "done":
			icon = "✓"
			color = m.theme.Success
		case "error":
			icon = "✗"
			color = m.theme.Error
		case "cancelled":
			icon = "✕"
			color = m.theme.Muted
		}
		label := trimTo(st.Title, width-8)
		if label == "" {
			label = "(running)"
		}
		tag := lipgloss.NewStyle().Bold(true).Foreground(color).Render(icon + " " + shortID(id))
		lines = append(lines, tag, lipgloss.NewStyle().Foreground(m.theme.Muted).Render("   "+label))
	}
	more := ""
	if start > 0 {
		more = "\n" + lipgloss.NewStyle().Foreground(m.theme.Muted).Render(
			"  "+itoa(start)+" older hidden — Ctrl+P to expand")
	}
	inner := title + "\n\n" + strings.Join(lines, "\n") + more
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent2).
		Padding(0, 1).
		Width(width - 2).
		Height(maxH - 2).
		Render(inner)
}

// renderExpandedPanel — full-screen activity browser (Ctrl+P view) with
// its own scrollable viewport + scrollbar. Up/down/pgup/pgdn scrolls.
func (m *Model) renderExpandedPanel() string {
	body := m.buildExpandedPanelBody()

	// Inner box dimensions after border + padding.
	innerW := m.width - 6 // 2 border + 4 horizontal padding
	if innerW < 10 {
		innerW = 10
	}
	innerH := m.height - 6 // 2 border + 2 vertical padding + 2 hint
	if innerH < 5 {
		innerH = 5
	}

	// Re-init viewport on first open or resize.
	if !m.panelViewInit || m.panelView.Width != innerW || m.panelView.Height != innerH {
		m.panelView = viewport.New(innerW, innerH)
		m.panelViewInit = true
	}
	m.panelView.SetContent(body)

	bar := scrollbar(&m.panelView, innerH, m.theme)
	content := lipgloss.JoinHorizontal(lipgloss.Top, m.panelView.View(), bar)

	hint := lipgloss.NewStyle().Foreground(m.theme.Muted).
		Render("↑↓/PgUp/PgDn scroll · Ctrl+P or Esc to close")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 2).
		Width(m.width - 2).
		Height(m.height - 2).
		Render(content + "\n" + hint)
}

// buildExpandedPanelBody composes the content string — separate function
// so the viewport can re-content when data changes.
func (m *Model) buildExpandedPanelBody() string {
	var sections []string
	codeTitle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(
		fmt.Sprintf("Code activity (%d events)", len(m.codeViews)))
	sections = append(sections, codeTitle)
	if len(m.codeViews) == 0 {
		sections = append(sections, lipgloss.NewStyle().Faint(true).Render("  (none yet)"))
	} else {
		for _, e := range m.codeViews {
			icon := "📄"
			if e.IsDiff {
				icon = "✏️ "
			} else if e.IsNew {
				icon = "🆕"
			}
			sections = append(sections, fmt.Sprintf("  %s %s · %s · %s",
				icon, e.Path, e.When.Format("15:04:05"), e.Text))
		}
	}
	sections = append(sections, "")
	saCount := 0
	if m.subagents != nil {
		saCount = len(m.subagents.Order)
	}
	saTitle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render(
		fmt.Sprintf("Subagents (%d tasks)", saCount))
	sections = append(sections, saTitle)
	if saCount == 0 {
		sections = append(sections, lipgloss.NewStyle().Faint(true).Render("  (none yet)"))
	} else {
		for _, id := range m.subagents.Order {
			st := m.subagents.Tasks[id]
			icon := "●"
			switch st.Status {
			case "done":
				icon = "✓"
			case "error":
				icon = "✗"
			case "cancelled":
				icon = "✕"
			}
			sections = append(sections, fmt.Sprintf("  %s %s — %s", icon, shortID(id), st.Title))
			for _, line := range st.Lines {
				sections = append(sections, lipgloss.NewStyle().Faint(true).Render("    "+trimTo(line, m.width-8)))
			}
		}
	}
	return strings.Join(sections, "\n")
}

func shortID(id string) string {
	if i := strings.LastIndex(id, "_"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func trimTo(s string, n int) string {
	if n <= 0 {
		return s
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func tailN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}

func asString(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}
