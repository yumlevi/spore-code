package app

import "github.com/charmbracelet/lipgloss"

// Theme is the minimal colour palette the app references. Port of the
// selection of themes from acorn/themes.py — simplified to what Bubble Tea
// currently renders reliably.
type Theme struct {
	Name string

	Fg         lipgloss.Color
	BgPanel    lipgloss.Color
	Muted      lipgloss.Color
	Accent     lipgloss.Color
	Accent2    lipgloss.Color
	Separator  lipgloss.Color

	UserPanel lipgloss.Color
	BotPanel  lipgloss.Color
	System    lipgloss.Color

	Success lipgloss.Color
	Warning lipgloss.Color
	Error   lipgloss.Color

	ToolIcon lipgloss.Color
	EditIcon lipgloss.Color
	ReadIcon lipgloss.Color
	Thinking lipgloss.Color

	ModeBarPlanBg lipgloss.Color
	ModeBarExecBg lipgloss.Color
	ToolbarHint   lipgloss.Color
}

var themeDark = Theme{
	Name: "dark",
	Fg:            "#c8cdd8",
	BgPanel:       "#0e1017",
	Muted:         "#7a8595",
	Accent:        "#5b8af5",
	Accent2:       "#8b6cf7",
	Separator:     "#1e2133",
	UserPanel:     "#5b8af5",
	BotPanel:      "#c8cdd8",
	System:        "#7a8595",
	Success:       "#4ade80",
	Warning:       "#f59e0b",
	Error:         "#f05858",
	ToolIcon:      "#8b6cf7",
	EditIcon:      "#f59e0b",
	ReadIcon:      "#38bdf8",
	Thinking:      "#7a8595",
	ModeBarPlanBg: "#8b6cf7",
	ModeBarExecBg: "#4ade80",
	ToolbarHint:   "#4a4f68",
}

var themeOak = Theme{
	Name:          "oak",
	Fg:            "#e8d5c0",
	BgPanel:       "#1a1614",
	Muted:         "#7a6a58",
	Accent:        "#f59e0b",
	Accent2:       "#ef4444",
	Separator:     "#3a2e24",
	UserPanel:     "#f59e0b",
	BotPanel:      "#e8d5c0",
	System:        "#7a6a58",
	Success:       "#84cc16",
	Warning:       "#f59e0b",
	Error:         "#ef4444",
	ToolIcon:      "#f59e0b",
	EditIcon:      "#f97316",
	ReadIcon:      "#eab308",
	Thinking:      "#7a6a58",
	ModeBarPlanBg: "#ef4444",
	ModeBarExecBg: "#84cc16",
	ToolbarHint:   "#5a4a38",
}

var themeForest = Theme{
	Name:          "forest",
	Fg:            "#c0dcc0",
	BgPanel:       "#0e1a0e",
	Muted:         "#4a7a4a",
	Accent:        "#4ade80",
	Accent2:       "#a3e635",
	Separator:     "#1e3a1e",
	UserPanel:     "#4ade80",
	BotPanel:      "#c0dcc0",
	System:        "#4a7a4a",
	Success:       "#22c55e",
	Warning:       "#eab308",
	Error:         "#ef4444",
	ToolIcon:      "#4ade80",
	EditIcon:      "#eab308",
	ReadIcon:      "#22d3ee",
	Thinking:      "#4a7a4a",
	ModeBarPlanBg: "#a3e635",
	ModeBarExecBg: "#4ade80",
	ToolbarHint:   "#2a5a2a",
}

var themeOled = Theme{
	Name:          "oled",
	Fg:            "#e5e5e5",
	BgPanel:       "#000000",
	Muted:         "#707070",
	Accent:        "#ffffff",
	Accent2:       "#d0d0d0",
	Separator:     "#222222",
	UserPanel:     "#ffffff",
	BotPanel:      "#e5e5e5",
	System:        "#707070",
	Success:       "#22c55e",
	Warning:       "#eab308",
	Error:         "#ef4444",
	ToolIcon:      "#d0d0d0",
	EditIcon:      "#eab308",
	ReadIcon:      "#38bdf8",
	Thinking:      "#707070",
	ModeBarPlanBg: "#404040",
	ModeBarExecBg: "#10b981",
	ToolbarHint:   "#505050",
}

var themeLight = Theme{
	Name:          "light",
	Fg:            "#1a1a1a",
	BgPanel:       "#f8f6f2",
	Muted:         "#6b6560",
	Accent:        "#2563eb",
	Accent2:       "#7c3aed",
	Separator:     "#c8c0b4",
	UserPanel:     "#2563eb",
	BotPanel:      "#1a1a1a",
	System:        "#6b6560",
	Success:       "#16a34a",
	Warning:       "#d97706",
	Error:         "#dc2626",
	ToolIcon:      "#7c3aed",
	EditIcon:      "#d97706",
	ReadIcon:      "#0284c7",
	Thinking:      "#6b6560",
	ModeBarPlanBg: "#7c3aed",
	ModeBarExecBg: "#16a34a",
	ToolbarHint:   "#a8a095",
}

var themeNeon = Theme{
	Name:          "neon",
	Fg:            "#e0d0f0",
	BgPanel:       "#0d0520",
	Muted:         "#6040a0",
	Accent:        "#ff2d95",
	Accent2:       "#00f0ff",
	Separator:     "#2a1050",
	UserPanel:     "#ff2d95",
	BotPanel:      "#e0d0f0",
	System:        "#6040a0",
	Success:       "#00f0ff",
	Warning:       "#ff2d95",
	Error:         "#ff2d55",
	ToolIcon:      "#00f0ff",
	EditIcon:      "#ff2d95",
	ReadIcon:      "#00f0ff",
	Thinking:      "#6040a0",
	ModeBarPlanBg: "#ff2d95",
	ModeBarExecBg: "#00f0ff",
	ToolbarHint:   "#4020a0",
}

var themeTerminal = Theme{
	Name:          "terminal",
	Fg:            "#33ff33",
	BgPanel:       "#0a0a0a",
	Muted:         "#1a6b1a",
	Accent:        "#66ff66",
	Accent2:       "#00cc00",
	Separator:     "#1a3a1a",
	UserPanel:     "#66ff66",
	BotPanel:      "#33ff33",
	System:        "#1a6b1a",
	Success:       "#33ff33",
	Warning:       "#ffcc00",
	Error:         "#ff3333",
	ToolIcon:      "#33ff33",
	EditIcon:      "#ffcc00",
	ReadIcon:      "#33ff33",
	Thinking:      "#1a6b1a",
	ModeBarPlanBg: "#00cc00",
	ModeBarExecBg: "#33ff33",
	ToolbarHint:   "#0f4a0f",
}

var themeArctic = Theme{
	Name:          "arctic",
	Fg:            "#0f172a",
	BgPanel:       "#f0f4f9",
	Muted:         "#5a6a80",
	Accent:        "#2563eb",
	Accent2:       "#4f46e5",
	Separator:     "#b0bad0",
	UserPanel:     "#2563eb",
	BotPanel:      "#0f172a",
	System:        "#5a6a80",
	Success:       "#16a34a",
	Warning:       "#d97706",
	Error:         "#dc2626",
	ToolIcon:      "#4f46e5",
	EditIcon:      "#d97706",
	ReadIcon:      "#0284c7",
	Thinking:      "#5a6a80",
	ModeBarPlanBg: "#4f46e5",
	ModeBarExecBg: "#16a34a",
	ToolbarHint:   "#8898b0",
}

var themeMidnight = Theme{
	Name:          "midnight",
	Fg:            "#c8cdd8",
	BgPanel:       "#08090e",
	Muted:         "#4a4f68",
	Accent:        "#5b8af5",
	Accent2:       "#8b6cf7",
	Separator:     "#1e2133",
	UserPanel:     "#5b8af5",
	BotPanel:      "#e2e6f0",
	System:        "#4a4f68",
	Success:       "#4ade80",
	Warning:       "#f59e0b",
	Error:         "#f05858",
	ToolIcon:      "#8b6cf7",
	EditIcon:      "#f59e0b",
	ReadIcon:      "#5b8af5",
	Thinking:      "#4a4f68",
	ModeBarPlanBg: "#8b6cf7",
	ModeBarExecBg: "#4ade80",
	ToolbarHint:   "#2a2d3a",
}

// themeForName returns the named theme, falling back to dark.
func themeForName(name string) Theme {
	switch name {
	case "oak":
		return themeOak
	case "forest":
		return themeForest
	case "oled":
		return themeOled
	case "light":
		return themeLight
	case "neon":
		return themeNeon
	case "terminal":
		return themeTerminal
	case "arctic":
		return themeArctic
	case "midnight":
		return themeMidnight
	case "dark", "":
		return themeDark
	}
	return themeDark
}

// ThemeNames returns the list shown by /theme.
func ThemeNames() []string {
	return []string{"dark", "midnight", "oak", "forest", "oled", "light", "neon", "terminal", "arctic"}
}
