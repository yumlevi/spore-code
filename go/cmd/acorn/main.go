// Command acorn — Go port of acorn-cli. See /go/README.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/acorn-cli/go/internal/app"
	"github.com/yumlevi/acorn-cli/go/internal/config"
)

var version = "0.1.0"

func main() {
	var (
		host      = flag.String("host", "", "SPORE server host (overrides config)")
		port      = flag.Int("port", 0, "SPORE web port (overrides config)")
		user      = flag.String("user", "", "username (overrides config)")
		sessionID = flag.String("session", "", "resume a specific session id")
		cont      = flag.Bool("continue", false, "resume the most recent session in this cwd")
		planMode  = flag.Bool("plan", false, "start in plan mode")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.BoolVar(cont, "c", false, "short for --continue")
	flag.Parse()

	if *showVer {
		fmt.Printf("acorn %s (go port)\n", version)
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fail("cannot read cwd:", err)
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		if err == config.ErrNoGlobalConfig {
			fmt.Fprintln(os.Stderr, "no global config at ~/.acorn/config.toml — running first-time setup")
			fresh, werr := runSetupWizard()
			if werr != nil {
				fail("setup wizard failed:", werr)
			}
			cfg = fresh
		} else {
			fail("config load failed:", err)
		}
	}

	if *host != "" {
		cfg.Connection.Host = *host
	}
	if *port != 0 {
		cfg.Connection.Port = *port
	}
	if *user != "" {
		cfg.Connection.User = *user
	}
	if cfg.Connection.User == "" {
		fail("no user — set `user` in ~/.acorn/config.toml [connection] or pass --user", nil)
	}
	if cfg.Connection.Key == "" {
		fail("no acorn team key — set `key` in ~/.acorn/config.toml [connection]", nil)
	}

	// Create .acorn/{plans,logs}/ in cwd so tools have somewhere to write.
	_ = config.EnsureLocalDir(cwd)
	_ = os.MkdirAll(filepath.Join(cwd, ".acorn", "logs"), 0o755)

	// Session resolution.
	sess := *sessionID
	if sess == "" && *cont {
		if last, err := config.LoadLastSession(cfg.GlobalDir); err == nil && last.SessionID != "" {
			sess = last.SessionID
		}
	}
	if sess == "" {
		sess = app.ComputeSessionID(cfg.Connection.User, cwd)
	}

	m := app.New(cfg, cwd, sess, *planMode)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.SetProgram(p)
	if _, err := p.Run(); err != nil {
		fail("fatal:", err)
	}
	// Save the session for next `-c`.
	_ = config.SaveLastSession(cfg.GlobalDir, sess, cwd)

	// Unused tree imports silencer (keep go vet happy if new imports drift).
	_ = context.Background
	_ = strings.TrimSpace
}

func fail(prefix string, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix, err)
	} else {
		fmt.Fprintln(os.Stderr, prefix)
	}
	os.Exit(1)
}
