package app

import "testing"

func TestThemeNamesExposeOnlyCurrentCoreThemes(t *testing.T) {
	got := ThemeNames()
	want := []string{"dark", "oled", "light"}
	if len(got) != len(want) {
		t.Fatalf("theme count mismatch: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("theme names mismatch: got %#v want %#v", got, want)
		}
	}
}

func TestLegacyThemeNamesNormalizeToDark(t *testing.T) {
	getenv := env(map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"})
	if got := themeForNameWithEnv("oak", getenv).Name; got != "dark" {
		t.Fatalf("legacy theme should normalize to dark, got %q", got)
	}
	if isThemeName("oak") {
		t.Fatalf("legacy theme should not be exposed as selectable")
	}
	if got := themeForNameWithEnv("oled", getenv).Name; got != "oled" {
		t.Fatalf("oled theme should be selectable, got %q", got)
	}
}

func TestWeakTerminalUsesCompatTheme(t *testing.T) {
	got := themeForNameWithEnv("dark", env(map[string]string{"TERM": "linux"}))
	if !got.Compat {
		t.Fatalf("linux console should use compat theme")
	}
	if got.Name != "dark-compat" {
		t.Fatalf("compat theme name mismatch: got %q", got.Name)
	}
	if got.Bg != "0" || got.Fg != "15" || got.Accent != "15" || got.PlanBarBg == "4" || got.Info == "14" {
		t.Fatalf("compat palette should avoid blue/cyan-heavy ANSI colors, got bg=%q fg=%q accent=%q planBarBg=%q info=%q", got.Bg, got.Fg, got.Accent, got.PlanBarBg, got.Info)
	}
	if saved := savedThemeName(got); saved != "dark" {
		t.Fatalf("compat theme should persist base name, got %q", saved)
	}
}

func TestTruecolorTerminalKeepsRgbTheme(t *testing.T) {
	got := themeForNameWithEnv("dark", env(map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}))
	if got.Compat {
		t.Fatalf("truecolor terminal should keep RGB theme")
	}
	if got.Name != "dark" {
		t.Fatalf("theme name mismatch: got %q", got.Name)
	}
	if got.Bg == "0" || got.Fg == "15" {
		t.Fatalf("truecolor theme should not use compat ANSI palette")
	}
}

func TestCompatThemeOverride(t *testing.T) {
	forcedOn := themeForNameWithEnv("light", env(map[string]string{
		"TERM":               "xterm-256color",
		"COLORTERM":          "truecolor",
		"SPORE_THEME_COMPAT": "1",
	}))
	if !forcedOn.Compat {
		t.Fatalf("SPORE_THEME_COMPAT=1 should force compat mode")
	}

	forcedOff := themeForNameWithEnv("light", env(map[string]string{
		"TERM":               "linux",
		"SPORE_THEME_COMPAT": "0",
	}))
	if forcedOff.Compat {
		t.Fatalf("SPORE_THEME_COMPAT=0 should disable compat mode")
	}
}

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
