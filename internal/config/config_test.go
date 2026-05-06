package config

import "testing"

func TestConnectionMethodDefaultsToInvite(t *testing.T) {
	c := ConnectionSection{User: "yam", Key: "invite-key"}
	if got := c.Method(); got != AuthInvite {
		t.Fatalf("expected invite auth, got %q", got)
	}
	if !c.HasCredentials() {
		t.Fatalf("expected invite credentials to be complete")
	}
}

func TestConnectionMethodSupportsPassword(t *testing.T) {
	c := ConnectionSection{User: "yam", AuthMethod: AuthPassword, Password: "secret"}
	if got := c.Method(); got != AuthPassword {
		t.Fatalf("expected password auth, got %q", got)
	}
	if !c.HasCredentials() {
		t.Fatalf("expected password credentials to be complete")
	}
}

func TestConnectionMethodInfersPasswordWhenOnlyPasswordIsSet(t *testing.T) {
	c := ConnectionSection{User: "yam", Password: "secret"}
	if got := c.Method(); got != AuthPassword {
		t.Fatalf("expected inferred password auth, got %q", got)
	}
}

func TestConnectionHasCredentialsRequiresSelectedSecret(t *testing.T) {
	if (ConnectionSection{User: "yam", AuthMethod: AuthPassword, Key: "invite-key"}).HasCredentials() {
		t.Fatalf("password auth should require password, not invite key")
	}
	if (ConnectionSection{User: "yam", AuthMethod: AuthInvite, Password: "secret"}).HasCredentials() {
		t.Fatalf("invite auth should require invite key, not password")
	}
}
