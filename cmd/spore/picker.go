package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yumlevi/spore-code/internal/sessionlog"
)

// sessionPicker is a small standalone Bubble Tea program shown before
// the main TUI when -c finds multiple project-local sessions. Returns
// the picked session id (or "" if cancelled).
type sessionPicker struct {
	items    []sessionlog.ProjectSession
	cursor   int
	width    int
	height   int
	chosen   string
	canceled bool
}

func (p *sessionPicker) Init() tea.Cmd { return nil }

func (p *sessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = m.Width, m.Height
	case tea.KeyMsg:
		switch m.String() {
		case "ctrl+c", "esc", "q":
			p.canceled = true
			return p, tea.Quit
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(p.items)-1 {
				p.cursor++
			}
		case "home", "g":
			p.cursor = 0
		case "end", "G":
			p.cursor = len(p.items) - 1
		case "enter":
			p.chosen = p.items[p.cursor].SessionID
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *sessionPicker) View() string {
	if p.width == 0 {
		return "loading…"
	}
	t := pickerTheme()
	header := lipgloss.NewStyle().Bold(true).Foreground(t.accent).
		Render("🌰 Resume a session")
	sub := lipgloss.NewStyle().Foreground(t.muted).
		Render(fmt.Sprintf("%d session(s) for this project · ↑↓ select · enter resume · esc cancel", len(p.items)))

	// Visible window — fit cursor.
	rows := p.height - 6
	if rows < 5 {
		rows = 5
	}
	if rows > len(p.items) {
		rows = len(p.items)
	}
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	end := start + rows
	if end > len(p.items) {
		end = len(p.items)
	}

	var lines []string
	for i := start; i < end; i++ {
		s := p.items[i]
		ageCol := lipgloss.NewStyle().Width(14).Foreground(t.fg).Render(s.TimeAgo)
		msgCol := lipgloss.NewStyle().Width(8).Align(lipgloss.Right).Foreground(t.muted).
			Render(fmt.Sprintf("%d msgs", s.MessageCount))
		preview := s.Preview
		maxPreview := p.width - 30
		if maxPreview < 20 {
			maxPreview = 20
		}
		if len(preview) > maxPreview {
			preview = preview[:maxPreview-1] + "…"
		}
		previewCol := lipgloss.NewStyle().Foreground(t.fg).Render(preview)

		row := ageCol + " " + msgCol + "  " + previewCol
		if i == p.cursor {
			row = lipgloss.NewStyle().
				Background(t.accent).Foreground(lipgloss.Color("#ffffff")).
				Bold(true).Padding(0, 1).
				Render("▸ " + ageCol + " " + msgCol + "  " + preview)
		} else {
			row = lipgloss.NewStyle().Padding(0, 1).Render("  " + row)
		}
		lines = append(lines, row)
	}

	indicator := ""
	if start > 0 || end < len(p.items) {
		indicator = lipgloss.NewStyle().Foreground(t.muted).Render(
			fmt.Sprintf("  showing %d-%d of %d (↑↓/g/G to navigate)", start+1, end, len(p.items)))
	}

	body := header + "\n" + sub + "\n\n" + lipgloss.JoinVertical(lipgloss.Left, lines...)
	if indicator != "" {
		body += "\n\n" + indicator
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.accent).
		Padding(1, 2).
		Width(p.width - 4).
		Render(body)
}

type pickerThemeT struct {
	accent lipgloss.Color
	fg     lipgloss.Color
	muted  lipgloss.Color
}

func pickerTheme() pickerThemeT {
	return pickerThemeT{
		accent: lipgloss.Color("#5b8af5"),
		fg:     lipgloss.Color("#c8cdd8"),
		muted:  lipgloss.Color("#7a8595"),
	}
}

// runSessionPicker launches the picker as a small alt-screen program.
// Returns the chosen session id, or ("", false) if the user cancelled.
func runSessionPicker(items []sessionlog.ProjectSession) (string, bool) {
	p := &sessionPicker{items: items}
	prog := tea.NewProgram(p, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		return "", false
	}
	if p.canceled || p.chosen == "" {
		return "", false
	}
	return p.chosen, true
}
