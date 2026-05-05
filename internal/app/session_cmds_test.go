package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yumlevi/spore-code/internal/config"
	"github.com/yumlevi/spore-code/internal/sessionlog"
)

func TestSwitchSessionReopensWriter(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		Connection: config.ConnectionSection{User: "yam"},
		GlobalDir:  filepath.Join(tmp, "global"),
	}
	m := &Model{cfg: cfg, cwd: tmp, currentStreamIdx: -1}

	switchSession(m, "session-one", false)
	firstPath := m.writer.Path()
	switchSession(m, "session-two", false)
	secondPath := m.writer.Path()
	if firstPath == secondPath {
		t.Fatalf("expected writer path to change, got %s", firstPath)
	}

	m.pushChat("user", "message after resume")

	firstEntries := sessionlog.LoadSession(cfg.GlobalDir, "session-one")
	for _, e := range firstEntries {
		if strings.Contains(e.Text, "message after resume") {
			t.Fatalf("message was written to old session: %#v", firstEntries)
		}
	}
	secondEntries := sessionlog.LoadSession(cfg.GlobalDir, "session-two")
	found := false
	for _, e := range secondEntries {
		if e.Role == "user" && e.Text == "message after resume" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("message not written to resumed session: %#v", secondEntries)
	}
}
