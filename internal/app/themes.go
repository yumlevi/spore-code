package app

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// Theme is the full semantic palette — direct port of acorn/themes.py.
// Field names follow Python's snake_case keys converted to Go's CamelCase
// so the two implementations can be diffed mechanically.
type Theme struct {
	Name string
	Icon string

	// Backgrounds
	Bg       lipgloss.Color
	BgHeader lipgloss.Color
	BgInput  lipgloss.Color
	BgPanel  lipgloss.Color

	// Core
	Fg     lipgloss.Color
	Border lipgloss.Color

	// Accents / status
	Accent  lipgloss.Color
	Accent2 lipgloss.Color
	Success lipgloss.Color
	Error   lipgloss.Color
	Warning lipgloss.Color
	Info    lipgloss.Color
	Muted   lipgloss.Color

	// Tools / diff
	ToolIcon lipgloss.Color
	ToolDone lipgloss.Color
	ReadIcon lipgloss.Color
	EditIcon lipgloss.Color
	DiffAdd  lipgloss.Color
	DiffDel  lipgloss.Color

	// Text
	Thinking      lipgloss.Color
	Usage         lipgloss.Color
	PromptUser    lipgloss.Color
	PromptProject lipgloss.Color
	PromptBranch  lipgloss.Color
	PromptSymbol  lipgloss.Color

	// Plan/exec mode label bars
	PlanBarFg   lipgloss.Color
	PlanBarBg   lipgloss.Color
	ExecBarFg   lipgloss.Color
	ExecBarBg   lipgloss.Color
	PlanLabelFg lipgloss.Color
	PlanLabelBg lipgloss.Color
	ExecLabelFg lipgloss.Color
	ExecLabelBg lipgloss.Color

	// Banner / misc
	Banner    lipgloss.Color
	BannerSub lipgloss.Color
	CodeTheme string
	Separator lipgloss.Color

	// Runtime compatibility mode. When true, the palette intentionally uses
	// ANSI 16 colors instead of RGB hex so weak/web terminals do not invent
	// inconsistent nearest-color mappings for every theme token.
	Compat bool

	// ── Compatibility shims for existing renderer call sites ────────
	// These alias the canonical fields above. They exist so I don't have
	// to touch every place that references the old names; new code
	// should use the canonical names.
	UserPanel     lipgloss.Color // = PromptUser
	BotPanel      lipgloss.Color // = Fg
	System        lipgloss.Color // = Muted
	ModeBarPlanBg lipgloss.Color // = PlanLabelBg
	ModeBarExecBg lipgloss.Color // = ExecLabelBg
	ToolbarHint   lipgloss.Color // = Muted
}

// derive sets the compat aliases from the canonical fields. Called once
// per theme literal so we never see them out of sync.
func (t Theme) derive() Theme {
	t.UserPanel = t.PromptUser
	t.BotPanel = t.Fg
	t.System = t.Muted
	t.ModeBarPlanBg = t.PlanLabelBg
	t.ModeBarExecBg = t.ExecLabelBg
	t.ToolbarHint = t.Muted
	return t
}

var themeDark = Theme{
	Name: "dark", Icon: "🌙",
	Bg: "#131511", BgHeader: "#1a1d17", BgInput: "#21241d", BgPanel: "#1a1d17",
	Fg: "#ece7d5", Border: "#2c2f26",
	Accent: "#8eb77c", Accent2: "#e38b5f",
	Success: "#8eb77c", Error: "#d94e3a", Warning: "#e38b5f", Info: "#7aa7d6", Muted: "#706c62",
	ToolIcon: "#e3b567", ToolDone: "#8eb77c", ReadIcon: "#7aa7d6", EditIcon: "#e38b5f",
	DiffAdd: "#8eb77c", DiffDel: "#e89488",
	Thinking: "#7aa7d6", Usage: "#706c62",
	PromptUser: "#8eb77c", PromptProject: "#ece7d5", PromptBranch: "#e38b5f", PromptSymbol: "#8eb77c",
	PlanBarFg: "#ece7d5", PlanBarBg: "#2a2d24", ExecBarFg: "#ece7d5", ExecBarBg: "#252d20",
	PlanLabelFg: "#131511", PlanLabelBg: "#e38b5f", ExecLabelFg: "#131511", ExecLabelBg: "#8eb77c",
	Banner: "#ece7d5", BannerSub: "#706c62", CodeTheme: "monokai", Separator: "#2c2f26",
}.derive()

var themeOled = Theme{
	Name: "oled", Icon: "⬛",
	Bg: "#000000", BgHeader: "#0a0a0a", BgInput: "#0a0a0a", BgPanel: "#0a0a0a",
	Fg: "#e0e0e0", Border: "#333333",
	Accent: "#ffffff", Accent2: "#bbbbbb",
	Success: "#ffffff", Error: "#ff4444", Warning: "#cccccc", Info: "#666666", Muted: "#666666",
	ToolIcon: "#cccccc", ToolDone: "#ffffff", ReadIcon: "#aaaaaa", EditIcon: "#cccccc",
	DiffAdd: "#ffffff", DiffDel: "#ff4444",
	Thinking: "#888888", Usage: "#555555",
	PromptUser: "#ffffff", PromptProject: "#cccccc", PromptBranch: "#aaaaaa", PromptSymbol: "#ffffff",
	PlanBarFg: "#000000", PlanBarBg: "#333333", ExecBarFg: "#000000", ExecBarBg: "#222222",
	PlanLabelFg: "#000000", PlanLabelBg: "#ffffff", ExecLabelFg: "#000000", ExecLabelBg: "#cccccc",
	Banner: "#ffffff", BannerSub: "#555555", CodeTheme: "monokai", Separator: "#333333",
}.derive()

var themeLight = Theme{
	Name: "light", Icon: "☀",
	Bg: "#ece7dc", BgHeader: "#f3eddc", BgInput: "#f7f1e0", BgPanel: "#f3eddc",
	Fg: "#1d211b", Border: "#d6cdbb",
	Accent: "#b8542a", Accent2: "#3a6aa3",
	Success: "#5a7a4a", Error: "#b8341c", Warning: "#b8542a", Info: "#3a6aa3", Muted: "#8a8c82",
	ToolIcon: "#b8542a", ToolDone: "#5a7a4a", ReadIcon: "#3a6aa3", EditIcon: "#b8542a",
	DiffAdd: "#5a7a4a", DiffDel: "#8a3a1f",
	Thinking: "#3a6aa3", Usage: "#8a8c82",
	PromptUser: "#b8542a", PromptProject: "#5a7a4a", PromptBranch: "#3a6aa3", PromptSymbol: "#b8542a",
	PlanBarFg: "#1d211b", PlanBarBg: "#ead7ca", ExecBarFg: "#1d211b", ExecBarBg: "#dde5d0",
	PlanLabelFg: "#f7f1e0", PlanLabelBg: "#b8542a", ExecLabelFg: "#f7f1e0", ExecLabelBg: "#5a7a4a",
	Banner: "#1d211b", BannerSub: "#8a8c82", CodeTheme: "friendly", Separator: "#d6cdbb",
}.derive()

var themeOak = Theme{
	Name: "oak", Icon: "🪵",
	Bg: "#2c2016", BgHeader: "#382a1e", BgInput: "#382a1e", BgPanel: "#382a1e",
	Fg: "#e8d5b7", Border: "#6b5240",
	Accent: "#e09060", Accent2: "#b8a080",
	Success: "#a3b87a", Error: "#d47070", Warning: "#d4a84a", Info: "#8a7560", Muted: "#8a7560",
	ToolIcon: "#d4a84a", ToolDone: "#a3b87a", ReadIcon: "#c09868", EditIcon: "#d4a84a",
	DiffAdd: "#a3b87a", DiffDel: "#d47070",
	Thinking: "#e09060", Usage: "#8a7560",
	PromptUser: "#e09060", PromptProject: "#a3b87a", PromptBranch: "#d4a84a", PromptSymbol: "#e09060",
	PlanBarFg: "#e8d5b7", PlanBarBg: "#4a3828", ExecBarFg: "#e8d5b7", ExecBarBg: "#3a4a28",
	PlanLabelFg: "#2c2016", PlanLabelBg: "#e09060", ExecLabelFg: "#2c2016", ExecLabelBg: "#a3b87a",
	Banner: "#e09060", BannerSub: "#8a7560", CodeTheme: "monokai", Separator: "#6b5240",
}.derive()

var themeForest = Theme{
	Name: "forest", Icon: "🌲",
	Bg: "#0c1f14", BgHeader: "#12301e", BgInput: "#12301e", BgPanel: "#12301e",
	Fg: "#b0d8b0", Border: "#2a5a3a",
	Accent: "#50c878", Accent2: "#78b090",
	Success: "#70e890", Error: "#e87070", Warning: "#e8d060", Info: "#4a7a5a", Muted: "#4a7a5a",
	ToolIcon: "#e8d060", ToolDone: "#70e890", ReadIcon: "#50c878", EditIcon: "#e8d060",
	DiffAdd: "#70e890", DiffDel: "#e87070",
	Thinking: "#50c878", Usage: "#4a7a5a",
	PromptUser: "#70e890", PromptProject: "#50c878", PromptBranch: "#e8d060", PromptSymbol: "#70e890",
	PlanBarFg: "#b0d8b0", PlanBarBg: "#1a4028", ExecBarFg: "#b0d8b0", ExecBarBg: "#285018",
	PlanLabelFg: "#0c1f14", PlanLabelBg: "#50c878", ExecLabelFg: "#0c1f14", ExecLabelBg: "#70e890",
	Banner: "#50c878", BannerSub: "#4a7a5a", CodeTheme: "monokai", Separator: "#2a5a3a",
}.derive()

var themeNord = Theme{
	Name: "nord", Icon: "❄",
	Bg: "#2e3440", BgHeader: "#3b4252", BgInput: "#3b4252", BgPanel: "#3b4252",
	Fg: "#d8dee9", Border: "#4c566a",
	Accent: "#88c0d0", Accent2: "#81a1c1",
	Success: "#a3be8c", Error: "#bf616a", Warning: "#ebcb8b", Info: "#616e88", Muted: "#616e88",
	ToolIcon: "#ebcb8b", ToolDone: "#a3be8c", ReadIcon: "#88c0d0", EditIcon: "#ebcb8b",
	DiffAdd: "#a3be8c", DiffDel: "#bf616a",
	Thinking: "#88c0d0", Usage: "#616e88",
	PromptUser: "#88c0d0", PromptProject: "#a3be8c", PromptBranch: "#ebcb8b", PromptSymbol: "#88c0d0",
	PlanBarFg: "#d8dee9", PlanBarBg: "#434c5e", ExecBarFg: "#d8dee9", ExecBarBg: "#3b4a3e",
	PlanLabelFg: "#2e3440", PlanLabelBg: "#88c0d0", ExecLabelFg: "#2e3440", ExecLabelBg: "#a3be8c",
	Banner: "#88c0d0", BannerSub: "#616e88", CodeTheme: "monokai", Separator: "#4c566a",
}.derive()

var themeDracula = Theme{
	Name: "dracula", Icon: "🧛",
	Bg: "#282a36", BgHeader: "#343746", BgInput: "#343746", BgPanel: "#343746",
	Fg: "#f8f8f2", Border: "#44475a",
	Accent: "#bd93f9", Accent2: "#ff79c6",
	Success: "#50fa7b", Error: "#ff5555", Warning: "#f1fa8c", Info: "#6272a4", Muted: "#6272a4",
	ToolIcon: "#f1fa8c", ToolDone: "#50fa7b", ReadIcon: "#8be9fd", EditIcon: "#f1fa8c",
	DiffAdd: "#50fa7b", DiffDel: "#ff5555",
	Thinking: "#bd93f9", Usage: "#6272a4",
	PromptUser: "#bd93f9", PromptProject: "#50fa7b", PromptBranch: "#f1fa8c", PromptSymbol: "#bd93f9",
	PlanBarFg: "#f8f8f2", PlanBarBg: "#44475a", ExecBarFg: "#f8f8f2", ExecBarBg: "#3a4a3e",
	PlanLabelFg: "#282a36", PlanLabelBg: "#bd93f9", ExecLabelFg: "#282a36", ExecLabelBg: "#50fa7b",
	Banner: "#bd93f9", BannerSub: "#6272a4", CodeTheme: "monokai", Separator: "#44475a",
}.derive()

var themeSunset = Theme{
	Name: "sunset", Icon: "🌅",
	Bg: "#1a1020", BgHeader: "#241830", BgInput: "#241830", BgPanel: "#241830",
	Fg: "#e8d0c0", Border: "#4a2a3a",
	Accent: "#ff7b72", Accent2: "#d2a8ff",
	Success: "#7ee787", Error: "#ff4466", Warning: "#ffa657", Info: "#7a5a6a", Muted: "#7a5a6a",
	ToolIcon: "#ffa657", ToolDone: "#7ee787", ReadIcon: "#ff7b72", EditIcon: "#ffa657",
	DiffAdd: "#7ee787", DiffDel: "#ff4466",
	Thinking: "#d2a8ff", Usage: "#7a5a6a",
	PromptUser: "#ff7b72", PromptProject: "#7ee787", PromptBranch: "#ffa657", PromptSymbol: "#ff7b72",
	PlanBarFg: "#e8d0c0", PlanBarBg: "#3a2040", ExecBarFg: "#e8d0c0", ExecBarBg: "#2a3a20",
	PlanLabelFg: "#1a1020", PlanLabelBg: "#d2a8ff", ExecLabelFg: "#1a1020", ExecLabelBg: "#7ee787",
	Banner: "#ff7b72", BannerSub: "#7a5a6a", CodeTheme: "monokai", Separator: "#4a2a3a",
}.derive()

var themeOcean = Theme{
	Name: "ocean", Icon: "🌊",
	Bg: "#0a1628", BgHeader: "#0e2040", BgInput: "#0e2040", BgPanel: "#0e2040",
	Fg: "#a8c8e8", Border: "#1a3a5a",
	Accent: "#40b0e0", Accent2: "#60d0a0",
	Success: "#60d0a0", Error: "#e06060", Warning: "#e0c060", Info: "#4a6a8a", Muted: "#4a6a8a",
	ToolIcon: "#e0c060", ToolDone: "#60d0a0", ReadIcon: "#40b0e0", EditIcon: "#e0c060",
	DiffAdd: "#60d0a0", DiffDel: "#e06060",
	Thinking: "#40b0e0", Usage: "#4a6a8a",
	PromptUser: "#40b0e0", PromptProject: "#60d0a0", PromptBranch: "#e0c060", PromptSymbol: "#40b0e0",
	PlanBarFg: "#a8c8e8", PlanBarBg: "#1a3050", ExecBarFg: "#a8c8e8", ExecBarBg: "#1a4030",
	PlanLabelFg: "#0a1628", PlanLabelBg: "#40b0e0", ExecLabelFg: "#0a1628", ExecLabelBg: "#60d0a0",
	Banner: "#40b0e0", BannerSub: "#4a6a8a", CodeTheme: "monokai", Separator: "#1a3a5a",
}.derive()

var themeCherry = Theme{
	Name: "cherry", Icon: "🍒",
	Bg: "#1a0a14", BgHeader: "#2a1020", BgInput: "#2a1020", BgPanel: "#2a1020",
	Fg: "#e8c8d8", Border: "#4a2838",
	Accent: "#ff6090", Accent2: "#c090ff",
	Success: "#80e0a0", Error: "#ff3060", Warning: "#ffc070", Info: "#7a4a60", Muted: "#7a4a60",
	ToolIcon: "#ffc070", ToolDone: "#80e0a0", ReadIcon: "#ff6090", EditIcon: "#ffc070",
	DiffAdd: "#80e0a0", DiffDel: "#ff3060",
	Thinking: "#c090ff", Usage: "#7a4a60",
	PromptUser: "#ff6090", PromptProject: "#80e0a0", PromptBranch: "#ffc070", PromptSymbol: "#ff6090",
	PlanBarFg: "#e8c8d8", PlanBarBg: "#3a1828", ExecBarFg: "#e8c8d8", ExecBarBg: "#1a3828",
	PlanLabelFg: "#1a0a14", PlanLabelBg: "#ff6090", ExecLabelFg: "#1a0a14", ExecLabelBg: "#80e0a0",
	Banner: "#ff6090", BannerSub: "#7a4a60", CodeTheme: "monokai", Separator: "#4a2838",
}.derive()

var themeCyber = Theme{
	Name: "cyber", Icon: "🔮",
	Bg: "#0a0a12", BgHeader: "#10101a", BgInput: "#10101a", BgPanel: "#10101a",
	Fg: "#b0f0b0", Border: "#1a2a1a",
	Accent: "#00ff88", Accent2: "#00ccff",
	Success: "#00ff88", Error: "#ff0055", Warning: "#ffcc00", Info: "#3a5a3a", Muted: "#3a5a3a",
	ToolIcon: "#ffcc00", ToolDone: "#00ff88", ReadIcon: "#00ccff", EditIcon: "#ffcc00",
	DiffAdd: "#00ff88", DiffDel: "#ff0055",
	Thinking: "#00ccff", Usage: "#3a5a3a",
	PromptUser: "#00ff88", PromptProject: "#00ccff", PromptBranch: "#ffcc00", PromptSymbol: "#00ff88",
	PlanBarFg: "#b0f0b0", PlanBarBg: "#0a1a0a", ExecBarFg: "#b0f0b0", ExecBarBg: "#0a0a1a",
	PlanLabelFg: "#0a0a12", PlanLabelBg: "#00ccff", ExecLabelFg: "#0a0a12", ExecLabelBg: "#00ff88",
	Banner: "#00ff88", BannerSub: "#3a5a3a", CodeTheme: "monokai", Separator: "#1a2a1a",
}.derive()

var themeGameboy = Theme{
	Name: "gameboy", Icon: "🎮",
	Bg: "#0f380f", BgHeader: "#306230", BgInput: "#306230", BgPanel: "#306230",
	Fg: "#9bbc0f", Border: "#8bac0f",
	Accent: "#9bbc0f", Accent2: "#8bac0f",
	Success: "#9bbc0f", Error: "#0f380f", Warning: "#8bac0f", Info: "#306230", Muted: "#306230",
	ToolIcon: "#8bac0f", ToolDone: "#9bbc0f", ReadIcon: "#9bbc0f", EditIcon: "#8bac0f",
	DiffAdd: "#9bbc0f", DiffDel: "#0f380f",
	Thinking: "#9bbc0f", Usage: "#306230",
	PromptUser: "#9bbc0f", PromptProject: "#8bac0f", PromptBranch: "#9bbc0f", PromptSymbol: "#9bbc0f",
	PlanBarFg: "#0f380f", PlanBarBg: "#8bac0f", ExecBarFg: "#0f380f", ExecBarBg: "#9bbc0f",
	PlanLabelFg: "#0f380f", PlanLabelBg: "#9bbc0f", ExecLabelFg: "#0f380f", ExecLabelBg: "#8bac0f",
	Banner: "#9bbc0f", BannerSub: "#306230", CodeTheme: "monokai", Separator: "#8bac0f",
}.derive()

var themeAmber = Theme{
	Name: "amber", Icon: "💾",
	Bg: "#0a0800", BgHeader: "#1a1400", BgInput: "#1a1400", BgPanel: "#1a1400",
	Fg: "#ffb000", Border: "#805800",
	Accent: "#ffb000", Accent2: "#cc8800",
	Success: "#ffb000", Error: "#ff4400", Warning: "#cc8800", Info: "#805800", Muted: "#805800",
	ToolIcon: "#cc8800", ToolDone: "#ffb000", ReadIcon: "#ffb000", EditIcon: "#cc8800",
	DiffAdd: "#ffb000", DiffDel: "#ff4400",
	Thinking: "#ffb000", Usage: "#805800",
	PromptUser: "#ffb000", PromptProject: "#cc8800", PromptBranch: "#ffb000", PromptSymbol: "#ffb000",
	PlanBarFg: "#0a0800", PlanBarBg: "#805800", ExecBarFg: "#0a0800", ExecBarBg: "#cc8800",
	PlanLabelFg: "#0a0800", PlanLabelBg: "#ffb000", ExecLabelFg: "#0a0800", ExecLabelBg: "#cc8800",
	Banner: "#ffb000", BannerSub: "#805800", CodeTheme: "monokai", Separator: "#805800",
}.derive()

var themePhosphor = Theme{
	Name: "phosphor", Icon: "📺",
	Bg: "#001100", BgHeader: "#002200", BgInput: "#002200", BgPanel: "#002200",
	Fg: "#33ff33", Border: "#116611",
	Accent: "#33ff33", Accent2: "#22cc22",
	Success: "#33ff33", Error: "#ff3333", Warning: "#22cc22", Info: "#116611", Muted: "#116611",
	ToolIcon: "#22cc22", ToolDone: "#33ff33", ReadIcon: "#33ff33", EditIcon: "#22cc22",
	DiffAdd: "#33ff33", DiffDel: "#ff3333",
	Thinking: "#33ff33", Usage: "#116611",
	PromptUser: "#33ff33", PromptProject: "#22cc22", PromptBranch: "#33ff33", PromptSymbol: "#33ff33",
	PlanBarFg: "#001100", PlanBarBg: "#116611", ExecBarFg: "#001100", ExecBarBg: "#22cc22",
	PlanLabelFg: "#001100", PlanLabelBg: "#33ff33", ExecLabelFg: "#001100", ExecLabelBg: "#22cc22",
	Banner: "#33ff33", BannerSub: "#116611", CodeTheme: "monokai", Separator: "#116611",
}.derive()

var themeC64 = Theme{
	Name: "c64", Icon: "🕹",
	Bg: "#40318d", BgHeader: "#503ca0", BgInput: "#503ca0", BgPanel: "#503ca0",
	Fg: "#a0a0ff", Border: "#7070cc",
	Accent: "#a0a0ff", Accent2: "#7070cc",
	Success: "#a0a0ff", Error: "#ff5050", Warning: "#7070cc", Info: "#6060aa", Muted: "#6060aa",
	ToolIcon: "#7070cc", ToolDone: "#a0a0ff", ReadIcon: "#a0a0ff", EditIcon: "#7070cc",
	DiffAdd: "#a0a0ff", DiffDel: "#ff5050",
	Thinking: "#a0a0ff", Usage: "#6060aa",
	PromptUser: "#a0a0ff", PromptProject: "#7070cc", PromptBranch: "#a0a0ff", PromptSymbol: "#a0a0ff",
	PlanBarFg: "#40318d", PlanBarBg: "#6060aa", ExecBarFg: "#40318d", ExecBarBg: "#7070cc",
	PlanLabelFg: "#40318d", PlanLabelBg: "#a0a0ff", ExecLabelFg: "#40318d", ExecLabelBg: "#7070cc",
	Banner: "#a0a0ff", BannerSub: "#6060aa", CodeTheme: "monokai", Separator: "#7070cc",
}.derive()

var themeSnes = Theme{
	Name: "snes", Icon: "🎮",
	Bg: "#2a2830", BgHeader: "#343240", BgInput: "#343240", BgPanel: "#302e3a",
	Fg: "#d8d4d0", Border: "#4a4850",
	Accent: "#b0a0e0", Accent2: "#6090e0",
	Success: "#60d060", Error: "#f04040", Warning: "#e0c040", Info: "#a09c98", Muted: "#a09c98",
	ToolIcon: "#e0c040", ToolDone: "#60d060", ReadIcon: "#6090e0", EditIcon: "#e0c040",
	DiffAdd: "#60d060", DiffDel: "#f04040",
	Thinking: "#b0a0e0", Usage: "#a09c98",
	PromptUser: "#b0a0e0", PromptProject: "#60d060", PromptBranch: "#e0c040", PromptSymbol: "#b0a0e0",
	PlanBarFg: "#f0ece8", PlanBarBg: "#6a6070", ExecBarFg: "#f0ece8", ExecBarBg: "#507050",
	PlanLabelFg: "#f0ece8", PlanLabelBg: "#8070b0", ExecLabelFg: "#f0ece8", ExecLabelBg: "#409040",
	Banner: "#b0a0e0", BannerSub: "#a09c98", CodeTheme: "monokai", Separator: "#584a70",
}.derive()

// themeForName returns the named theme, falling back to dark. Older saved
// theme names that are no longer exposed intentionally normalize to dark.
func themeForName(name string) Theme {
	return themeForNameWithEnv(name, os.Getenv)
}

func themeForNameWithEnv(name string, getenv func(string) string) Theme {
	base := baseThemeForName(name)
	if shouldUseCompatTheme(getenv) {
		return compatTheme(base)
	}
	return base
}

func baseThemeForName(name string) Theme {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dark", "":
		return themeDark
	case "oled":
		return themeOled
	case "light":
		return themeLight
	}
	return themeDark
}

func shouldUseCompatTheme(getenv func(string) string) bool {
	forced := strings.ToLower(strings.TrimSpace(getenv("SPORE_THEME_COMPAT")))
	switch forced {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}

	term := strings.ToLower(strings.TrimSpace(getenv("TERM")))
	colorTerm := strings.ToLower(strings.TrimSpace(getenv("COLORTERM")))

	if strings.Contains(colorTerm, "truecolor") || strings.Contains(colorTerm, "24bit") {
		return false
	}
	if term == "" || term == "dumb" || term == "linux" {
		return true
	}
	return !strings.Contains(term, "256color")
}

func compatTheme(t Theme) Theme {
	t.Name = t.Name + "-compat"
	t.Icon = "◐"
	t.Compat = true

	// Keep compat mode deliberately plain. Some appliance consoles (notably
	// Unraid's web terminal) downsample ANSI colors unpredictably; bright cyan
	// and blue-heavy blocks turn into a harsh black/blue UI. Prefer neutral
	// foreground/background colors with small green/yellow/red accents.
	t.Bg = "0"
	t.BgHeader = "0"
	t.BgInput = "0"
	t.BgPanel = "0"
	t.Fg = "15"
	t.Border = "7"
	t.Accent = "15"
	t.Accent2 = "7"
	t.Success = "2"
	t.Error = "1"
	t.Warning = "3"
	t.Info = "7"
	t.Muted = "7"
	t.ToolIcon = "3"
	t.ToolDone = "2"
	t.ReadIcon = "7"
	t.EditIcon = "3"
	t.DiffAdd = "2"
	t.DiffDel = "1"
	t.Thinking = "15"
	t.Usage = "7"
	t.PromptUser = "15"
	t.PromptProject = "2"
	t.PromptBranch = "3"
	t.PromptSymbol = "7"
	t.PlanBarFg = "0"
	t.PlanBarBg = "7"
	t.ExecBarFg = "0"
	t.ExecBarBg = "2"
	t.PlanLabelFg = "0"
	t.PlanLabelBg = "7"
	t.ExecLabelFg = "0"
	t.ExecLabelBg = "2"
	t.Banner = "15"
	t.BannerSub = "7"
	t.Separator = "7"
	t.CodeTheme = "ansi"

	return t.derive()
}

func isThemeName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, "-compat")
	switch name {
	case "dark", "oled", "light":
		return true
	default:
		return false
	}
}

func savedThemeName(t Theme) string {
	return strings.TrimSuffix(t.Name, "-compat")
}

// ThemeNames returns the order shown by /theme.
func ThemeNames() []string {
	return []string{"dark", "oled", "light"}
}

// AllThemes returns every exposed theme value — used by the wizard for swatches.
func AllThemes() []Theme {
	return []Theme{themeDark, themeOled, themeLight}
}

func (m *Model) applyTheme(t Theme) {
	m.theme = t
	applyTextareaTheme(&m.input, t)
	m.historyDirty = true
	m.viewportDirty = true
	m.renderedHistory = ""
	m.historyWidth = -1
}

func applyTextareaTheme(input *textarea.Model, t Theme) {
	focused, blurred := textareaStylesForTheme(t)
	input.FocusedStyle = focused
	input.BlurredStyle = blurred
	input.Cursor.Style = lipgloss.NewStyle().Foreground(t.BgInput).Background(t.Accent)
	input.Cursor.TextStyle = lipgloss.NewStyle().Foreground(t.Fg)
	if input.Focused() {
		_ = input.Focus()
	} else {
		input.Blur()
	}
}

func textareaStylesForTheme(t Theme) (textarea.Style, textarea.Style) {
	base := lipgloss.NewStyle().Foreground(t.Fg)
	muted := lipgloss.NewStyle().Foreground(t.Muted)
	prompt := lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
	focused := textarea.Style{
		Base:        base,
		CursorLine:  lipgloss.NewStyle(),
		EndOfBuffer: muted,
		Placeholder: muted.Italic(true),
		Prompt:      prompt,
		Text:        base,
	}
	blurred := textarea.Style{
		Base:        base,
		CursorLine:  lipgloss.NewStyle(),
		EndOfBuffer: muted,
		Placeholder: muted.Italic(true),
		Prompt:      prompt,
		Text:        muted,
	}
	return focused, blurred
}
