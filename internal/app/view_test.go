package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"

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
