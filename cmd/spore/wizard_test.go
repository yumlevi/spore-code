package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestTestAuthUsesPasswordPayload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/spore-code/auth" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"token":"ok"}`))
	}))
	defer srv.Close()

	if err := testAuth(srv.URL, 0, "yam", config.AuthPassword, "", "secret"); err != nil {
		t.Fatalf("test auth: %v", err)
	}
	if got["username"] != "yam" || got["password"] != "secret" || got["authMethod"] != config.AuthPassword {
		t.Fatalf("password auth payload mismatch: %#v", got)
	}
	if _, ok := got["key"]; ok {
		t.Fatalf("password auth should not send invite key: %#v", got)
	}
}
