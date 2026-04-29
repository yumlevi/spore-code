package app

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// scrollbar renders a single-column vertical bar showing the current
// scroll position in a viewport. Call it with the same height as the
// viewport — returns `height` lines.
//
// Characters:
//   track — faint vertical bar ("│") in the theme's separator color
//   thumb — solid block ("█") in the accent color, sized proportional to
//           the visible fraction of total content
//
// If there's no overflow (content fits), returns height empty lines so
// the layout stays rectangular.
func scrollbar(vp *viewport.Model, height int, t Theme) string {
	if height <= 0 {
		return ""
	}
	total := vp.TotalLineCount()
	visible := vp.VisibleLineCount()

	trackChar := "│"
	thumbChar := "█"
	trackStyle := lipgloss.NewStyle().Foreground(t.Separator)
	thumbStyle := lipgloss.NewStyle().Foreground(t.Accent)

	// No overflow — return a blank column so column widths stay aligned.
	if total == 0 || total <= visible {
		return strings.Repeat(" \n", height-1) + " "
	}

	// Thumb height — proportional to visible / total, minimum 1.
	thumbH := (visible * height) / total
	if thumbH < 1 {
		thumbH = 1
	}
	if thumbH > height {
		thumbH = height
	}

	// Thumb top position — proportional to YOffset within scrollable range.
	maxOffset := total - visible
	thumbTop := 0
	if maxOffset > 0 {
		thumbTop = (vp.YOffset * (height - thumbH)) / maxOffset
	}
	if thumbTop+thumbH > height {
		thumbTop = height - thumbH
	}

	// Build the column.
	lines := make([]string, height)
	for i := 0; i < height; i++ {
		if i >= thumbTop && i < thumbTop+thumbH {
			lines[i] = thumbStyle.Render(thumbChar)
		} else {
			lines[i] = trackStyle.Render(trackChar)
		}
	}
	return strings.Join(lines, "\n")
}

