package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// codeViewEntry tracks a single file view/diff event the agent emitted
// via chat:tool / code:view / code:diff. Recent entries surface in a
// right-side panel so the operator can watch what the agent is touching.
type codeViewEntry struct {
	Path   string
	Text   string // either full content (view) or a diff summary
	IsDiff bool
	IsNew  bool
	When   time.Time
}

// pushCodeView records a read_file / write_file hit.
func (m *Model) pushCodeView(path, content string, isNew bool) {
	lineCount := strings.Count(content, "\n") + 1
	preview := fmt.Sprintf("%d lines, %d bytes", lineCount, len(content))
	m.codeViews = append(m.codeViews, codeViewEntry{
		Path: path, Text: preview, IsNew: isNew, When: time.Now(),
	})
	if len(m.codeViews) > 20 {
		m.codeViews = m.codeViews[len(m.codeViews)-20:]
	}
}

// pushCodeDiff records an edit_file hit.
func (m *Model) pushCodeDiff(path, oldT, newT string) {
	added := strings.Count(newT, "\n")
	removed := strings.Count(oldT, "\n")
	preview := fmt.Sprintf("+%d / -%d lines", added, removed)
	m.codeViews = append(m.codeViews, codeViewEntry{
		Path: path, Text: preview, IsDiff: true, When: time.Now(),
	})
	if len(m.codeViews) > 20 {
		m.codeViews = m.codeViews[len(m.codeViews)-20:]
	}
}

// renderCodePanel returns the rendered side panel (empty string if no entries).
func (m *Model) renderCodePanel(width, height int) string {
	if len(m.codeViews) == 0 {
		return ""
	}
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render("Code activity")
	b.WriteString(title + "\n\n")
	start := 0
	if len(m.codeViews) > height-4 {
		start = len(m.codeViews) - (height - 4)
	}
	for _, e := range m.codeViews[start:] {
		icon := "📄"
		if e.IsDiff {
			icon = "✏️"
		} else if e.IsNew {
			icon = "🆕"
		}
		ts := e.When.Format("15:04:05")
		line := fmt.Sprintf("%s %s %s\n   %s\n",
			icon, e.Path,
			lipgloss.NewStyle().Foreground(m.theme.Muted).Render(ts),
			lipgloss.NewStyle().Foreground(m.theme.Muted).Render(e.Text))
		b.WriteString(line)
	}
	return borderStyle.Copy().
		BorderForeground(m.theme.Accent2).
		Width(width - 2).
		Height(height).
		Padding(1, 1).
		Render(b.String())
}

// subagent panel — tracks subagent:* messages the server streams for
// delegate_task runs.
type subagentPanel struct {
	Tasks map[string]*subagentState
	Order []string // insertion order for deterministic rendering
}

type subagentState struct {
	TaskID   string
	Title    string
	Status   string // running / done / error / cancelled
	Lines    []string
	Updated  time.Time
}

func newSubagentPanel() *subagentPanel {
	return &subagentPanel{Tasks: map[string]*subagentState{}}
}

// handleSubagentFrame routes "subagent:<verb>" ws messages.
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
			if len(st.Lines) > 10 {
				st.Lines = st.Lines[len(st.Lines)-10:]
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

func (m *Model) renderSubagentPanel(width, height int) string {
	if m.subagents == nil || len(m.subagents.Order) == 0 {
		return ""
	}
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render("Subagents")
	b.WriteString(title + "\n\n")
	for _, id := range m.subagents.Order {
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
		tag := lipgloss.NewStyle().Bold(true).Foreground(color).Render(icon + " " + shortID(id))
		b.WriteString(tag + " " + trimTo(st.Title, 40) + "\n")
		for _, line := range tailN(st.Lines, 3) {
			b.WriteString(lipgloss.NewStyle().Foreground(m.theme.Muted).Render("   " + trimTo(line, width-6)) + "\n")
		}
	}
	return borderStyle.Copy().
		BorderForeground(m.theme.Accent2).
		Width(width - 2).
		Height(height).
		Padding(1, 1).
		Render(b.String())
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

func tailN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}

// asString is a tiny helper mirroring tools/executor.go's asString — we
// duplicate to avoid an import cycle.
func asString(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}
