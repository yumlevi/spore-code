package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/spore-code/internal/config"
	"github.com/yumlevi/spore-code/internal/sessionlog"
)

// legacy_cmds.go — handlers for slash commands that used to live in the
// inline switch in update.go (`handleSlashCommand`). Migrated into the
// registry (commands.go's `register()` flow) so /help renders the full
// command set automatically and the inline switch shrinks to its
// default-case fallback.
//
// Functional behavior is identical to the pre-migration inline cases.
// New handlers can drop straight in here without touching update.go.

// /clear — wipe local message buffer + tell server to clear its session.
func cmdClear(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.messages = m.messages[:0]
	m.historyDirty = true
	m.rerenderViewport()
	_ = m.client.Send(map[string]any{"type": "chat:clear", "sessionId": m.sess})
	return m, nil
}

// /new — start a fresh session in this cwd. Old session is preserved on
// disk and remains reachable via /resume <id>.
func cmdNew(m *Model, _ []string) (tea.Model, tea.Cmd) {
	prev := m.sess
	m.sess = ComputeSessionIDFresh(m.cfg.Connection.User, m.cwd)
	m.messages = m.messages[:0]
	m.contextSent = false
	m.historyDirty = true
	if m.writer != nil {
		m.writer.Close()
	}
	if w, err := sessionlog.Open(m.cfg.GlobalDir, m.sess); err == nil {
		m.writer = w
	} else {
		m.writer = nil
	}
	if m.dlog != nil {
		m.dlog.Close()
	}
	m.dlog = sessionlog.OpenDebug(m.cfg.GlobalDir, m.sess, m.cfg.Connection.User, m.cwd)
	m.rerenderViewport()
	m.pushChat("system", "New session: "+m.sess)
	if prev != "" && prev != m.sess {
		m.pushChat("system", "Previous session preserved: "+prev+"  (use /resume "+prev+" to return)")
	}
	return m, nil
}

// /resume <sessionId> — switch to a different session id and replay
// local JSONL history.
func cmdResume(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) < 1 {
		m.pushChat("system", "Usage: /resume <sessionId>")
		return m, nil
	}
	m.sess = args[0]
	m.messages = m.messages[:0]
	m.historyDirty = true
	m.loadLocalHistory()
	_ = m.client.Send(map[string]any{"type": "chat:history-request", "sessionId": m.sess, "userName": m.cfg.Connection.User})
	m.pushChat("system", "Resumed: "+m.sess)
	return m, nil
}

// /quit — graceful close: kill bg procs, send session:end, close logs,
// quit the program.
func cmdQuit(m *Model, _ []string) (tea.Model, tea.Cmd) {
	closeSessionAndLogs(m)
	return m, tea.Quit
}

// /logout — clear the saved team key and quit. Next launch runs the
// first-time wizard since no key is configured.
func cmdLogout(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.cfg.Connection.Key = ""
	if err := config.Save(m.cfg); err != nil {
		m.pushChat("system", "Logout: failed to save config: "+err.Error())
		return m, nil
	}
	m.pushChat("system", "Logged out — connection credentials cleared from "+m.cfg.GlobalDir+"/config.toml. Run `spore` again to set up new credentials.")
	closeSessionAndLogs(m)
	return m, tea.Quit
}

// closeSessionAndLogs is the shared shutdown sequence used by /quit and
// /logout. Mirrors the original inline /quit case verbatim.
func closeSessionAndLogs(m *Model) {
	if m.exec != nil && m.exec.BG != nil {
		m.exec.BG.KillAll()
	}
	_ = m.client.Send(map[string]any{
		"type":      "session:end",
		"sessionId": m.sess,
		"endedAt":   time.Now().UTC().Format(time.RFC3339),
	})
	m.client.Close()
	if m.dlog != nil {
		m.dlog.Close()
	}
	if m.writer != nil {
		m.writer.Close()
	}
}

// /stop — abort the current generation locally + tell server.
func cmdStop(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.exec.AbortCurrent()
	_ = m.client.Send(map[string]any{"type": "chat:stop", "sessionId": m.sess})
	m.pushChat("system", "Stop requested.")
	return m, nil
}

// /plan — toggle between plan and execute mode.
func cmdPlan(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.planMode = !m.planMode
	label := "execute"
	if m.planMode {
		label = "plan"
	}
	m.pushChat("system", "Mode → "+label)
	return m, nil
}

// /status — print server / session / mode summary.
func cmdStatus(m *Model, _ []string) (tea.Model, tea.Cmd) {
	host := m.cfg.Connection.Host
	target := host
	if !strings.Contains(host, "://") {
		target = fmt.Sprintf("%s:%d", host, m.cfg.Connection.Port)
	}
	m.pushChat("system", fmt.Sprintf("server=%s user=%s session=%s planMode=%t mode=%s",
		target, m.cfg.Connection.User, m.sess, m.planMode, m.perms.Mode()))
	return m, nil
}

// /theme [name] — print current + available, or switch + persist.
func cmdTheme(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.pushChat("system", "Current: "+m.theme.Name+"\nAvailable: "+strings.Join(ThemeNames(), ", "))
		return m, nil
	}
	m.theme = themeForName(args[0])
	m.cfg.Display.Theme = m.theme.Name
	if err := config.Save(m.cfg); err != nil {
		m.pushChat("system", "Theme → "+m.theme.Name+"  (save failed: "+err.Error()+")")
	} else {
		m.pushChat("system", "Theme → "+m.theme.Name+"  (saved)")
	}
	m.historyDirty = true
	m.rerenderViewport()
	return m, nil
}

// /mode <auto|ask|locked|yolo|rules> — switch tool-approval mode.
func cmdMode(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.pushChat("system", "Usage: /mode <auto|ask|locked|yolo|rules>")
		return m, nil
	}
	switch args[0] {
	case "auto":
		m.perms.SetMode(PermAuto)
		m.pushChat("system", "Perms → auto (non-dangerous auto-approved)")
	case "ask":
		m.perms.SetMode(PermAsk)
		m.pushChat("system", "Perms → ask (prompt per call)")
	case "locked":
		m.perms.SetMode(PermLocked)
		m.pushChat("system", "Perms → locked (deny all writes/exec)")
	case "yolo":
		m.perms.SetMode(PermYolo)
		m.pushChat("system", "Perms → yolo (approve everything)")
	case "rules":
		rs := m.perms.Rules()
		if len(rs) == 0 {
			m.pushChat("system", "No session allow rules")
		} else {
			m.pushChat("system", "Allow rules:\n"+strings.Join(rs, "\n"))
		}
	default:
		m.pushChat("system", "Unknown mode: "+args[0])
	}
	return m, nil
}

// /approve-all — shortcut for /mode auto.
func cmdApproveAll(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.perms.SetMode(PermAuto)
	m.pushChat("system", "Perms → auto")
	return m, nil
}

// /approve-all-dangerous — shortcut for /mode yolo.
func cmdApproveAllDangerous(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.perms.SetMode(PermYolo)
	m.pushChat("system", "Perms → yolo")
	return m, nil
}

// /update [check|install [tag|substring]|list] — release management.
func cmdUpdate(m *Model, args []string) (tea.Model, tea.Cmd) {
	switch {
	case len(args) >= 1 && args[0] == "install":
		query := ""
		if len(args) >= 2 {
			query = strings.Join(args[1:], " ")
		}
		if query == "" {
			m.pushChat("system", "Installing latest stable release… will replace the running binary in place.")
			return m, installUpdateCmd("")
		}
		m.pushChat("system", fmt.Sprintf("Resolving release for %q… will install in place when found.", query))
		return m, resolveAndInstallCmd(query)
	case len(args) >= 1 && args[0] == "check":
		m.pushChat("system", "Checking GitHub releases…")
		return m, checkUpdateCmd(true)
	case len(args) >= 1 && (args[0] == "list" || args[0] == "channels"):
		m.pushChat("system", "Fetching release list from GitHub (includes pre-releases)…")
		return m, fetchAllReleasesCmd()
	default:
		m.pushChat("system", strings.Join([]string{
			"Usage:",
			"  /update check                       check the stable channel for a newer release",
			"  /update install                     install the latest STABLE release",
			"  /update install <tag>               install an exact tag (e.g. v1.0.2)",
			"  /update install pre                 install the latest pre-release (any kind)",
			"  /update list                        list recent releases (stable + pre-release)",
			"You're on " + Version + ".",
		}, "\n"))
		return m, nil
	}
}

// /bg [list|<id>|run <command>|kill <id>] — background process manager.
func cmdBg(m *Model, args []string) (tea.Model, tea.Cmd) {
	if m.exec == nil || m.exec.BG == nil {
		m.pushChat("system", "Background manager not available")
		return m, nil
	}
	if len(args) == 0 || args[0] == "list" {
		procs := m.exec.BG.List()
		if len(procs) == 0 {
			m.pushChat("system", "No background processes")
			return m, nil
		}
		var lines []string
		for _, p := range procs {
			st := "running"
			if !p.Running {
				st = fmt.Sprintf("exited (%d)", p.ExitCode)
			}
			cmd := p.Command
			if len(cmd) > 80 {
				cmd = cmd[:80]
			}
			lines = append(lines, fmt.Sprintf("  #%d  %s  %s  %s", p.ID, st, p.Elapsed(), cmd))
		}
		m.pushChat("system", strings.Join(lines, "\n"))
		return m, nil
	}
	sub := args[0]
	if sub == "kill" && len(args) >= 2 {
		var id int
		fmt.Sscanf(args[1], "%d", &id)
		if m.exec.BG.Kill(id) {
			m.pushChat("system", fmt.Sprintf("Killed #%d", id))
		} else {
			m.pushChat("system", fmt.Sprintf("Process #%d not found or already stopped", id))
		}
		return m, nil
	}
	if sub == "run" && len(args) >= 2 {
		cmd := strings.Join(args[1:], " ")
		if p, err := m.exec.BG.Launch(cmd, m.cwd); err != nil {
			m.pushChat("system", "Launch failed: "+err.Error())
		} else {
			m.pushChat("system", fmt.Sprintf("Launched #%d — log: %s", p.ID, p.LogPath))
		}
		return m, nil
	}
	// /bg <id> — show output
	var id int
	if _, err := fmt.Sscanf(sub, "%d", &id); err == nil {
		p := m.exec.BG.Get(id)
		if p == nil {
			m.pushChat("system", fmt.Sprintf("Process #%d not found", id))
			return m, nil
		}
		out := strings.Join(p.Output(), "\n")
		if len(out) > 4000 {
			out = "…" + out[len(out)-4000:]
		}
		m.pushChat("system", fmt.Sprintf("#%d  %s\n%s", p.ID, p.Elapsed(), out))
		return m, nil
	}
	m.pushChat("system", "Usage: /bg [list|<id>|run <command>|kill <id>]")
	return m, nil
}

// /sessions — list saved local sessions for this project (from JSONL logs).
func cmdSessions(m *Model, _ []string) (tea.Model, tea.Cmd) {
	root := findGitRoot(m.cwd)
	if root == "" {
		root = m.cwd
	}
	list := sessionlog.ListProjectSessions(m.cfg.GlobalDir, m.cfg.Connection.User, root)
	if len(list) == 0 {
		m.pushChat("system", "No saved sessions for this project")
		return m, nil
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Sessions for this project (%d):", len(list)))
	for i, s := range list {
		if i >= 15 {
			break
		}
		lines = append(lines, fmt.Sprintf("  %2d. %-12s %3d msgs  %s", i+1, s.TimeAgo, s.MessageCount, truncateFor(s.Preview, 60)))
	}
	lines = append(lines, "", "/resume <sessionId> to pick one")
	m.pushChat("system", strings.Join(lines, "\n"))
	return m, nil
}

// /decisions — hint stub. Actual ADR storage lives server-side in the
// graph; the agent has decisions_list / decisions_new / decisions_get /
// decisions_update tools. /decisions just nudges the user toward asking.
func cmdDecisions(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 || args[0] == "list" {
		m.pushChat("system",
			"Project decisions (ADRs) live in the graph. Ask the agent: "+
				"\"list decisions for this project\" — it will call decisions_list. "+
				"To create one: \"record a decision: <title>\" → decisions_new. "+
				"To fetch one: \"get decision <id>\" → decisions_get.")
		return m, nil
	}
	rest := strings.Join(args[1:], " ")
	if rest == "" {
		rest = "<args>"
	}
	m.pushChat("system",
		fmt.Sprintf("Ask the agent: \"%s decision %s\" — it will call decisions_%s.",
			args[0], rest, args[0]))
	return m, nil
}

// init registers every legacy-migrated handler. Lives in init() rather
// than a global var so the order matches how /help (which sorts by name)
// would render and so adding a new handler is a single-block change.
func init() {
	register(&slashCmd{Name: "/clear", Help: "clear chat history", Handler: cmdClear})
	register(&slashCmd{Name: "/new", Help: "start a fresh session in this cwd", Handler: cmdNew})
	register(&slashCmd{Name: "/resume", Help: "/resume <sessionId> — resume a specific session", Handler: cmdResume})
	register(&slashCmd{Name: "/sessions", Help: "list saved sessions for this project", Handler: cmdSessions})
	register(&slashCmd{Name: "/quit", Aliases: []string{"/exit", "/q"}, Help: "exit", Handler: cmdQuit})
	register(&slashCmd{Name: "/logout", Help: "clear saved credentials and exit (next launch re-runs first-time wizard)", Handler: cmdLogout})
	register(&slashCmd{Name: "/stop", Help: "stop the current generation", Handler: cmdStop})
	register(&slashCmd{Name: "/plan", Help: "toggle plan/execute mode (same as Shift+Tab)", Handler: cmdPlan})
	register(&slashCmd{Name: "/status", Help: "connection + session info", Handler: cmdStatus})
	register(&slashCmd{Name: "/theme", Help: "switch theme (dark/oak/forest/oled/light/…)", Handler: cmdTheme})
	register(&slashCmd{Name: "/mode", Help: "tool approval mode (auto/ask/locked/yolo/rules)", Handler: cmdMode})
	register(&slashCmd{Name: "/approve-all", Help: "shortcut for /mode auto", Handler: cmdApproveAll})
	register(&slashCmd{Name: "/approve-all-dangerous", Help: "shortcut for /mode yolo", Handler: cmdApproveAllDangerous})
	register(&slashCmd{Name: "/update", Help: "check/install/list releases", Handler: cmdUpdate})
	register(&slashCmd{Name: "/bg", Help: "background process list / run / kill", Handler: cmdBg})
	register(&slashCmd{Name: "/decisions", Help: "list / create / get project decisions (ADRs) — graph-backed", Handler: cmdDecisions})
}
