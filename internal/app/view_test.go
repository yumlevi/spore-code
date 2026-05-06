package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/yumlevi/spore-code/internal/config"
	"github.com/yumlevi/spore-code/internal/proto"
)

func TestRerenderViewportDoesNotTopPadShortTranscript(t *testing.T) {
	m := &Model{
		messages:         []chatMsg{{Role: "system", Text: "hello"}},
		currentStreamIdx: -1,
		viewport:         viewport.New(40, 5),
		theme:            themeDark,
		historyDirty:     true,
		followBottom:     true,
	}
	m.viewport.Width = 40
	m.viewport.Height = 5

	m.rerenderViewport()

	if got := m.viewport.View(); strings.HasPrefix(got, "\n") {
		t.Fatalf("expected transcript to start at top without artificial padding, got %q", got)
	}
}

func TestAppendDeltaIgnoresLeadingWhitespaceOnlyChunk(t *testing.T) {
	m := &Model{currentStreamIdx: -1}
	m.appendDelta(" \n\t")
	if m.currentStreamIdx != -1 || len(m.messages) != 0 {
		t.Fatalf("expected no stream for leading whitespace-only delta, idx=%d messages=%#v", m.currentStreamIdx, m.messages)
	}

	m.appendDelta("hello")
	if m.currentStreamIdx != 0 || len(m.messages) != 1 || m.messages[0].Text != "hello" {
		t.Fatalf("expected stream to start on visible text, idx=%d messages=%#v", m.currentStreamIdx, m.messages)
	}
}

func TestEndStreamTrimsTrailingWhitespace(t *testing.T) {
	m := &Model{currentStreamIdx: -1}
	m.appendDelta("first answer\n\n")
	m.endStream()

	if got := m.messages[0].Text; got != "first answer" {
		t.Fatalf("expected trimmed assistant text, got %q", got)
	}
}

func TestToolExecStartClosesAssistantSegment(t *testing.T) {
	m := &Model{currentStreamIdx: -1}
	m.appendDelta("before tool")
	m.handleStatus(proto.ChatStatus{Status: "tool_exec_start", Tool: "exec", Detail: "pwd"})

	if m.currentStreamIdx != -1 {
		t.Fatalf("expected tool start to close stream, idx=%d", m.currentStreamIdx)
	}
	if len(m.messages) != 1 || m.messages[0].Streaming || m.messages[0].Text != "before tool" {
		t.Fatalf("expected completed first segment, messages=%#v", m.messages)
	}

	m.appendDelta("after tool")
	if len(m.messages) != 2 || m.messages[1].Text != "after tool" {
		t.Fatalf("expected new segment after tool, messages=%#v", m.messages)
	}
}

func TestAppendActivityHonorsDisplayFlags(t *testing.T) {
	off := false
	m := &Model{
		cfg: &config.Config{
			Display: config.DisplaySection{ShowThinking: &off, ShowTools: &off},
		},
	}

	m.appendThinking("hidden thought")
	m.appendToolExec("exec", "pwd")
	m.pushCodeView("main.go", "package main", false)

	if len(m.codeViews) != 0 {
		t.Fatalf("expected display flags to suppress activity entries, got %#v", m.codeViews)
	}
}

func TestFormatUsageSummary(t *testing.T) {
	got := formatUsageSummary(proto.ChatDone{
		Usage: &proto.Usage{
			InputTokens:              100,
			OutputTokens:             25,
			CacheReadInputTokens:     10,
			CacheCreationInputTokens: 5,
		},
		Iterations: 2,
		ToolUsage:  map[string]int{"read_file": 3, "exec": 1},
	})
	want := "Usage: 100 in · 25 out · 10 cache-read · 5 cache-write · 2 iterations · tools exec×1, read_file×3"
	if got != want {
		t.Fatalf("unexpected usage summary\nwant: %q\n got: %q", want, got)
	}
}

func TestViewRenderFitsTerminalWithDenseWorkflow(t *testing.T) {
	for _, size := range []struct {
		w int
		h int
	}{
		{40, 12},
		{80, 24},
		{120, 30},
	} {
		t.Run(itoa(size.w)+"x"+itoa(size.h), func(t *testing.T) {
			m := renderTestModel(size.w, size.h)
			m.messages = []chatMsg{
				{Role: "system", Text: strings.Repeat("index 日本語 emoji 🎉 ", 20)},
				{Role: "assistant", Text: "## Plan\n\n" + strings.Repeat("Long rendered markdown with unicode — and a verylongtokenwithoutspaces ", 18)},
			}
			m.openQuestionModal([]question{{
				Text:    strings.Repeat("Choose the deployment target with a very long explanation ", 4),
				Options: []string{"Production cluster with a very long descriptive option", "Local docker compose", "Skip for now"},
			}})
			m.appendActivity(codeViewEntry{
				Path:    "internal/app/rendering/very/long/path/with-日本語-and-emoji-🎉.go",
				Preview: strings.Repeat("preview line with wide chars 日本語 🎉 ", 8),
				Text:    "activity with a long status tail",
				When:    time.Now(),
			})
			m.planTasks = newPlanTaskPanel()
			m.handleTaskFrame("create", map[string]any{"id": "task_001", "subject": strings.Repeat("implement stable renderer ", 5), "status": "in_progress"})
			m.handleTaskFrame("update", map[string]any{"id": "task_001", "note": strings.Repeat("note 日本語 🎉 ", 8)})

			assertViewFits(t, m)
		})
	}
}

func TestOutputLogScrollSurvivesRender(t *testing.T) {
	m := renderTestModel(80, 18)
	m.outputLogOpen = true
	for i := 0; i < 80; i++ {
		m.outputLog = append(m.outputLog, "line "+itoa(i)+" "+strings.Repeat("output ", 8))
	}

	_ = m.View()
	m.outputLogVP.LineUp(5)
	m.outputLogFollow = false
	before := m.outputLogVP.YOffset
	if before <= 0 {
		t.Fatalf("expected output log to scroll up, offset=%d", before)
	}
	_ = m.View()
	if got := m.outputLogVP.YOffset; got != before {
		t.Fatalf("render reset output log scroll offset: before=%d after=%d", before, got)
	}
}

func TestSidePanelsStayBoundedWithMultilineActivityRows(t *testing.T) {
	m := renderTestModel(120, 20)
	m.appendActivity(codeViewEntry{
		Tool:    "exec",
		ExecCmd: "printf 'one\\ntwo\\nthree'",
		Preview: strings.Join([]string{
			"tool output line 1",
			"tool output line 2",
			"tool output line 3",
			"tool output line 4",
			"tool output line 5",
			"tool output line 6",
		}, "\n"),
		Text: "tool\nwith accidental newline",
		When: time.Now(),
	})
	m.planTasks = newPlanTaskPanel()
	m.handleTaskFrame("create", map[string]any{
		"id":      "task_multiline",
		"subject": "first line\nsecond line\nthird line",
		"status":  "in_progress",
	})
	m.handleTaskFrame("update", map[string]any{
		"id":   "task_multiline",
		"note": "note line 1\nnote line 2\nnote line 3\nnote line 4",
	})

	const bodyH = 9
	side := m.renderSidePanelsBounded(bodyH)
	if side == "" {
		t.Fatalf("expected side panel to render")
	}
	if got := lipgloss.Height(side); got > bodyH {
		t.Fatalf("side panel exceeded body height: got %d want <= %d\n%s", got, bodyH, side)
	}
	assertBottomBorderVisible(t, side)
	assertViewFits(t, m)
}

func TestActivityPanelBottomBorderVisibleWhenContentIsTall(t *testing.T) {
	m := renderTestModel(120, 20)
	m.appendActivity(codeViewEntry{
		Tool: "exec",
		Preview: strings.Join([]string{
			"output 01",
			"output 02",
			"output 03",
			"output 04",
			"output 05",
			"output 06",
			"output 07",
			"output 08",
			"output 09",
			"output 10",
		}, "\n"),
		Text: "tool",
		When: time.Now(),
	})

	const bodyH = 9
	panel := m.renderCodePanel(m.codePanelWidth(), bodyH)
	if got := lipgloss.Height(panel); got != bodyH {
		t.Fatalf("activity panel height mismatch: got %d want %d\n%s", got, bodyH, panel)
	}
	assertBottomBorderVisible(t, panel)
}

func TestApplyThemeStylesInputAndInvalidatesHistory(t *testing.T) {
	m := renderTestModel(80, 20)
	m.renderedHistory = "cached"
	m.historyDirty = false
	m.viewportDirty = false
	m.historyWidth = 80

	m.applyTheme(themeLight)

	if got := fmt.Sprint(m.input.FocusedStyle.Text.GetForeground()); got != fmt.Sprint(themeLight.Fg) {
		t.Fatalf("input foreground was not themed: got %s want %s", got, themeLight.Fg)
	}
	if got := fmt.Sprint(m.input.FocusedStyle.Prompt.GetForeground()); got != fmt.Sprint(themeLight.Accent) {
		t.Fatalf("input prompt was not accented: got %s want %s", got, themeLight.Accent)
	}
	if !m.historyDirty || !m.viewportDirty || m.renderedHistory != "" || m.historyWidth != -1 {
		t.Fatalf("theme change did not invalidate render cache: dirty=%t viewportDirty=%t history=%q width=%d",
			m.historyDirty, m.viewportDirty, m.renderedHistory, m.historyWidth)
	}
}

func TestThemeMarkdownDoesNotEmitBlackBackgroundPatches(t *testing.T) {
	msg := chatMsg{
		Role: "assistant",
		Text: "Here is code:\n\n```go\nfmt.Println(\"hi\")\n```\n\nDone.",
	}
	rendered := renderMessage(msg, 80, themeLight)
	for _, needle := range []string{"\x1b[40m", "\x1b[48;5;0m", "\x1b[48;2;0;0;0m"} {
		if strings.Contains(rendered, needle) {
			t.Fatalf("rendered markdown contains black background escape %q:\n%q", needle, rendered)
		}
	}
}

func TestViewPaintsBaseBackgroundWithoutResetHoles(t *testing.T) {
	m := renderTestModel(80, 20)
	m.applyTheme(themeLight)
	m.messages = []chatMsg{{
		Role: "assistant",
		Text: "Here is code:\n\n```go\nfmt.Println(\"hi\")\n```\n\nDone.",
	}}

	out := m.View()
	bg := backgroundOpen(themeLight.Bg)
	if bg == "" || !strings.Contains(out, bg) {
		t.Fatalf("view did not paint base theme background")
	}
	if strings.Contains(out, "\x1b[0m") && !strings.Contains(out, "\x1b[0m"+bg) {
		t.Fatalf("view has ANSI resets that do not restore base background:\n%q", out)
	}
	assertViewFits(t, m)
}

func TestLogoMessageUsesThemeAccent(t *testing.T) {
	rendered := renderMessage(chatMsg{Role: "system", Text: LogoFull}, 100, themeDark)
	if !strings.HasPrefix(rendered, "\n") {
		t.Fatalf("logo banner should start with a blank separator line:\n%q", rendered)
	}
	accent := foregroundOpen(themeDark.Accent)
	if accent == "" || !strings.Contains(rendered, accent) {
		t.Fatalf("logo banner was not accented:\n%q", rendered)
	}
	if muted := foregroundOpen(themeDark.Muted); muted != "" && strings.Contains(rendered, muted) {
		t.Fatalf("logo banner should not render as muted system text:\n%q", rendered)
	}
}

func TestActiveFooterShowsWorkingIndicator(t *testing.T) {
	m := renderTestModel(120, 24)
	m.agentName = "Spore Sage"
	m.startActiveTurn("running tests")
	m.activeSince = time.Now().Add(-65 * time.Second)
	m.spinnerFrame = 3

	rendered := m.renderFooter()
	plain := ansi.Strip(rendered)
	for _, needle := range []string{"⠸ Spore Sage working", "1m0", "running tests", "Ctrl+C to stop"} {
		if !strings.Contains(plain, needle) {
			t.Fatalf("active footer missing %q:\n%q", needle, rendered)
		}
	}
	if strings.Contains(plain, "enter send") {
		t.Fatalf("active footer should replace idle shortcuts:\n%q", rendered)
	}
	if strings.ContainsAny(plain, "●•") {
		t.Fatalf("active footer should animate text color, not pulse glyphs:\n%q", rendered)
	}
	if strings.Count(rendered, "\x1b[38;2;") < 2 {
		t.Fatalf("active footer should wave accent colors across text:\n%q", rendered)
	}
}

func TestActiveFooterShowsThinkingTokens(t *testing.T) {
	m := renderTestModel(120, 24)
	m.agentName = "Spore Sage"
	m.startActiveTurn("thinking…")
	m.activeSince = time.Now().Add(-2 * time.Second)
	m.thinking = true
	m.thinkingTokens = 42
	m.spinnerFrame = 4

	rendered := m.renderFooter()
	plain := ansi.Strip(rendered)
	for _, needle := range []string{"Spore Sage thinking", "0m0", "42 thinking tokens", "Ctrl+C to stop"} {
		if !strings.Contains(plain, needle) {
			t.Fatalf("thinking footer missing %q:\n%q", needle, rendered)
		}
	}
	if strings.Contains(plain, "thinking…") {
		t.Fatalf("thinking footer should avoid duplicate raw status:\n%q", rendered)
	}
	if strings.ContainsAny(plain, "●•") {
		t.Fatalf("thinking footer should animate text color, not pulse glyphs:\n%q", rendered)
	}
	if strings.Count(rendered, "\x1b[38;2;") < 2 {
		t.Fatalf("thinking footer should wave thinking accent colors across text:\n%q", rendered)
	}
}

func TestActiveFooterStatusAvoidsFullAnsiResets(t *testing.T) {
	m := renderTestModel(120, 24)
	m.startActiveTurn("running tests")

	status := m.activeFooterStatus()
	if strings.Contains(status, "\x1b[0m") {
		t.Fatalf("active footer status should not reset background mid-line:\n%q", status)
	}
}

func TestActiveFooterAccentWaveMovesLeftToRight(t *testing.T) {
	const width = 24
	leftEarly := activeWaveColor(themeDark, 0, 0, width, false)
	leftLater := activeWaveColor(themeDark, 5, 0, width, false)
	rightEarly := activeWaveColor(themeDark, 0, 10, width, false)
	rightLater := activeWaveColor(themeDark, 5, 10, width, false)
	if leftEarly == leftLater {
		t.Fatalf("left edge should fade as wave moves right: early=%s later=%s", leftEarly, leftLater)
	}
	if rightEarly == rightLater {
		t.Fatalf("right side should brighten as wave moves right: early=%s later=%s", rightEarly, rightLater)
	}
	first := activeTextWave("Spore Sage working", themeDark, 0, false)
	second := activeTextWave("Spore Sage working", themeDark, 5, false)
	if ansi.Strip(first) != ansi.Strip(second) {
		t.Fatalf("wave should preserve text: %q vs %q", first, second)
	}
	if first == second {
		t.Fatalf("wave should change color placement across frames")
	}
}

func TestAssistantMessageUsesServerAgentName(t *testing.T) {
	rendered := renderMessageWithAgent(chatMsg{Role: "assistant", Text: "hello"}, 80, themeDark, "Spore Sage")
	plain := ansi.Strip(rendered)
	if !strings.Contains(plain, "Spore Sage") {
		t.Fatalf("assistant label did not use server agent name:\n%q", rendered)
	}
}

func TestAgentNameFromCapabilities(t *testing.T) {
	if got := agentNameFromCapabilities(nil, proto.ServerCapabilities{AgentDisplayName: " Spore   Sage "}); got != "Spore Sage" {
		t.Fatalf("typed capability agent name mismatch: %q", got)
	}
	raw := json.RawMessage(`{"type":"capabilities","agent":{"displayName":"Cedar"}}`)
	if got := agentNameFromCapabilities(raw, proto.ServerCapabilities{}); got != "Cedar" {
		t.Fatalf("nested capability agent name mismatch: %q", got)
	}
}

func renderTestModel(w, h int) *Model {
	ta := textarea.New()
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()
	m := &Model{
		cfg: &config.Config{
			Connection: config.ConnectionSection{User: "tester", Host: "localhost", Port: 18810},
			Display:    config.DisplaySection{Theme: "dark"},
		},
		cwd:              "/tmp/spore-render-test",
		sess:             "cli:tester@spore-render-test-0123456789abcdef",
		currentStreamIdx: -1,
		viewport:         viewport.New(0, 0),
		input:            ta,
		width:            w,
		height:           h,
		theme:            themeDark,
		gitBranch:        "main",
		followBottom:     true,
		outputLogFollow:  true,
	}
	return m
}

func assertViewFits(t *testing.T, m *Model) {
	t.Helper()
	got := m.View()
	lines := strings.Split(got, "\n")
	if len(lines) != m.height {
		t.Fatalf("render height mismatch: want %d lines, got %d\n%s", m.height, len(lines), got)
	}
	for i, line := range lines {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("line %d exceeds terminal width: got %d want <= %d\n%s", i+1, width, m.width, line)
		}
	}
}

func assertBottomBorderVisible(t *testing.T, block string) {
	t.Helper()
	lines := strings.Split(block, "\n")
	if len(lines) == 0 {
		t.Fatalf("expected rendered block")
	}
	last := ansi.Strip(lines[len(lines)-1])
	if !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("bottom frame is not visible: %q\n%s", last, block)
	}
}
