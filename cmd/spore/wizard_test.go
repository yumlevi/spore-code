package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/yumlevi/spore-code/internal/config"
)

func TestCleanPromptLineStripsBracketedPaste(t *testing.T) {
	got := cleanPromptLine("\x1b[200~invite-key-123\x1b[201~\r\n")
	if got != "invite-key-123" {
		t.Fatalf("expected bracketed paste wrappers stripped, got %q", got)
	}
}

func TestPromptAuthMethod(t *testing.T) {
	rd := bufio.NewReader(strings.NewReader("password\n"))
	if got := promptAuthMethod(rd, config.AuthInvite); got != config.AuthPassword {
		t.Fatalf("expected password auth, got %q", got)
	}
	rd = bufio.NewReader(strings.NewReader("\n"))
	if got := promptAuthMethod(rd, config.AuthPassword); got != config.AuthPassword {
		t.Fatalf("expected default password auth, got %q", got)
	}
}
