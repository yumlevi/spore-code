package app

import (
	"strings"
	"testing"
)

func TestBottomAlignContentPadsShortTranscript(t *testing.T) {
	got := bottomAlignContent("hello", 4)
	if strings.Count(got, "\n")+1 != 4 {
		t.Fatalf("expected 4 display lines, got %q", got)
	}
	if !strings.HasSuffix(got, "hello") {
		t.Fatalf("expected content at bottom, got %q", got)
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
