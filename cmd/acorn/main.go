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

	"github.com/yumlevi/acorn-cli/internal/app"
	"github.com/yumlevi/acorn-cli/internal/config"
	"github.com/yumlevi/acorn-cli/internal/sessionlog"
)

// version is overrideable at link time:
//   go build -ldflags "-X main.version=v0.1.1" ./cmd/acorn
// Falls back to the in-source default for plain `go build`.
var version = "v0.3.4-codeindex"

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
	app.SetVersion(version)

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
	//
	// Matches acorn/cli.py:async_main behaviour:
	//   explicit --session=<id>   → use it
	//   -c with 1 project session → auto-resume, print a line
	//   -c with >1 project sessions → interactive picker
	//   -c with 0 project sessions → fall back to ~/.acorn/last_session
	//   otherwise                 → fresh session id
	sess := *sessionID
	if sess == "" && *cont {
		projectRoot := app.FindGitRoot(cwd)
		if projectRoot == "" {
			projectRoot = cwd
		}
		list := sessionlog.ListProjectSessions(cfg.GlobalDir, cfg.Connection.User, projectRoot)
		switch {
		case len(list) == 1:
			sess = list[0].SessionID
			fmt.Printf("Resuming: %s  (%s)\n",
				truncate(list[0].Preview, 60), list[0].TimeAgo)
		case len(list) > 1:
			picked, ok := runSessionPicker(list)
			if !ok {
				fmt.Fprintln(os.Stderr, "cancelled")
				os.Exit(1)
			}
			sess = picked
		default:
			if last, err := config.LoadLastSession(cfg.GlobalDir); err == nil && last.SessionID != "" {
				sess = last.SessionID
				fmt.Printf("Resuming last session: %s\n", last.SessionID)
			} else {
				fmt.Fprintln(os.Stderr, "no previous session found — starting fresh")
			}
		}
	}
	if sess == "" {
		// Default: each launch gets a fresh, timestamped session so that
		// `acorn` doesn't silently drop you into your last conversation.
		// Set [session] auto_resume = true in config.toml to opt back into
		// the deterministic-id behavior; -c / --session=<id> are the
		// explicit "continue" gestures regardless.
		if cfg.Session.AutoResume {
			sess = app.ComputeSessionID(cfg.Connection.User, cwd)
		} else {
			sess = app.ComputeSessionIDFresh(cfg.Connection.User, cwd)
		}
	}

	// isContinue is true when the user passed -c/--continue OR gave an
	// explicit --session=<id>. In either case we should replay the local
	// JSONL history on boot so prior turns show up immediately.
	isContinue := *cont || *sessionID != ""
	m := app.New(cfg, cwd, sess, *planMode, isContinue)
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

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
