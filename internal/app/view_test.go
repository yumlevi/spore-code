package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
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
