package conn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticateUsesInviteKeyPayload(t *testing.T) {
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

	c := New(srv.URL, 0, "yam", "invite", "invite-key", "")
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got["username"] != "yam" || got["key"] != "invite-key" {
		t.Fatalf("invite auth payload mismatch: %#v", got)
	}
	if _, ok := got["password"]; ok {
		t.Fatalf("invite auth should not send password: %#v", got)
	}
}

func TestAuthenticateUsesPasswordPayload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"token":"ok"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 0, "yam", "password", "", "secret")
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got["username"] != "yam" || got["password"] != "secret" {
		t.Fatalf("password auth payload mismatch: %#v", got)
	}
	if _, ok := got["key"]; ok {
		t.Fatalf("password auth should not send invite key: %#v", got)
	}
	if _, ok := got["authMethod"]; ok {
		t.Fatalf("password auth should keep payload minimal: %#v", got)
	}
}
