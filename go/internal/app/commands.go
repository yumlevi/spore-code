package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/acorn-cli/go/internal/tools"
)

// slashHandler is the signature every slash command implements. The
// rest of the args (already split on whitespace) is passed verbatim;
// the original full text is available via the model if needed.
type slashHandler func(m *Model, args []string) (tea.Model, tea.Cmd)

// slashCmd is what /help renders.
type slashCmd struct {
	Name    string
	Aliases []string
	Help    string
	Handler slashHandler
}

// slashRegistry is the full command catalog. Populated in init() so
// tests + main code share the same map and any new command added here
// shows up in /help and the autocomplete dropdown automatically.
var slashRegistry = map[string]*slashCmd{}
var slashOrder []*slashCmd // stable display order for /help

// register adds a command to the registry. Called from init() blocks.
func register(e *slashCmd) {
	slashRegistry[e.Name] = e
	for _, a := range e.Aliases {
		slashRegistry[a] = e
	}
	slashOrder = append(slashOrder, e)
}

// dispatchSlash looks up the leading token, falls back to nil if
// unknown. update.go's handleSlashCommand wraps this to keep the
// existing call shape intact.
func dispatchSlash(m *Model, text string) (tea.Model, tea.Cmd, bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return m, nil, false
	}
	e, ok := slashRegistry[parts[0]]
	if !ok {
		return m, nil, false
	}
	mm, c := e.Handler(m, parts[1:])
	return mm, c, true
}

// SlashCatalog returns command names in display order — used by the
// autocomplete dropdown.
func SlashCatalog() []string {
	out := make([]string, 0, len(slashOrder))
	for _, e := range slashOrder {
		out = append(out, e.Name)
	}
	return out
}

// SlashHelpFromRegistry renders the /help body straight from the
// registry — guaranteed in sync with what's actually wired.
func SlashHelpFromRegistry() string {
	// Stable sort by name for the help block. Display-order in dropdown
	// keeps insertion order; help block alphabetises so it's scannable.
	names := make([]string, 0, len(slashOrder))
	maxLen := 0
	for _, e := range slashOrder {
		names = append(names, e.Name)
		if l := len(e.Name); l > maxLen {
			maxLen = l
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		e := slashRegistry[n]
		fmt.Fprintf(&b, "%-*s  %s\n", maxLen, n, e.Help)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── Built-in handlers that don't already live in update.go ──────────
//
// /context — show the gathered project context block + offer to refresh.
// /tree    — print a depth-limited file tree.
// /init    — write ACORN.md template + add .acorn/ to .gitignore.
// /help    — overrides update.go's static /help with the registry view.

func cmdContext(m *Model, args []string) (tea.Model, tea.Cmd) {
	ctx := GatherContext(m.cwd)
	m.pushChat("system", "── Project context ──\n"+ctx)
	if len(args) > 0 && args[0] == "refresh" {
		m.contextSent = false
		m.pushChat("system", "(context will be re-sent on next message)")
	}
	return m, nil
}

func cmdTree(m *Model, args []string) (tea.Model, tea.Cmd) {
	depth := 3
	if len(args) > 0 {
		if d, err := strconv.Atoi(args[0]); err == nil && d > 0 && d < 8 {
			depth = d
		}
	}
	m.pushChat("system", "── Project tree (depth "+strconv.Itoa(depth)+") ──\n"+treeString(m.cwd, depth, 200))
	return m, nil
}

func cmdInit(m *Model, args []string) (tea.Model, tea.Cmd) {
	path := filepath.Join(m.cwd, "ACORN.md")
	if _, err := os.Stat(path); err == nil {
		m.pushChat("system", "ACORN.md already exists at "+path)
		return m, nil
	}
	body := "# Project Instructions for Acorn\n\n" +
		"<!-- Add project-specific context here. Acorn sends this to the agent. -->\n\n" +
		"## Overview\n\n## Conventions\n\n## Important files\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		m.pushChat("system", "Failed to write ACORN.md: "+err.Error())
		return m, nil
	}
	m.pushChat("system", "Created "+path)
	gi := filepath.Join(m.cwd, ".gitignore")
	if _, err := os.Stat(gi); err == nil {
		if cur, err := os.ReadFile(gi); err == nil && !strings.Contains(string(cur), ".acorn/") {
			f, err := os.OpenFile(gi, os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				_, _ = f.WriteString("\n# Acorn local data\n.acorn/\n")
				_ = f.Close()
				m.pushChat("system", "Added .acorn/ to .gitignore")
			}
		}
	}
	return m, nil
}

func cmdHelp(m *Model, _ []string) (tea.Model, tea.Cmd) {
	m.pushChat("system", SlashHelpFromRegistry())
	return m, nil
}

// treeString — minimal, allocation-light port of acorn/context.py:_tree.
// Skips hidden dirs, common build/cache trees, files over 100 KB.
func treeString(root string, maxDepth, maxEntries int) string {
	skipDirs := map[string]struct{}{
		".git": {}, "node_modules": {}, ".venv": {}, "venv": {},
		"__pycache__": {}, "dist": {}, "build": {}, ".acorn": {},
		"target": {}, ".next": {}, ".cache": {},
	}
	var b strings.Builder
	count := 0
	var walk func(dir, prefix string, depth int)
	walk = func(dir, prefix string, depth int) {
		if depth > maxDepth || count >= maxEntries {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		// Sort: dirs first then files, both alpha.
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if a.IsDir() != b.IsDir() {
				return a.IsDir()
			}
			return a.Name() < b.Name()
		})
		n := len(entries)
		for i, e := range entries {
			if count >= maxEntries {
				b.WriteString(prefix + "└── …\n")
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") && name != ".env" && name != ".gitignore" {
				continue
			}
			if _, skip := skipDirs[name]; skip {
				continue
			}
			isLast := i == n-1
			branch := "├── "
			nextPrefix := prefix + "│   "
			if isLast {
				branch = "└── "
				nextPrefix = prefix + "    "
			}
			b.WriteString(prefix + branch + name + "\n")
			count++
			if e.IsDir() {
				walk(filepath.Join(dir, name), nextPrefix, depth+1)
			}
		}
	}
	b.WriteString(filepath.Base(root) + "/\n")
	walk(root, "", 1)
	return b.String()
}

// cmdScope toggles the file-op sandbox.
//
//	/scope                show current
//	/scope strict         lock file ops to cwd (default)
//	/scope expanded       allow file ops anywhere on the user's machine
//
// "Expanded" turns off both acorn's local cwd-containment check (in
// tools/fileops.go:ResolvePathScoped) AND the "sandboxed to cwd"
// instruction the agent sees in the system prompt. Use when you want
// the agent to read/write outside the project root — e.g., shared
// dotfiles, a sibling repo, your home directory.
func cmdScope(m *Model, args []string) (tea.Model, tea.Cmd) {
	arg := ""
	if len(args) > 0 {
		arg = strings.ToLower(args[0])
	}
	current := m.scope
	if current == "" {
		current = "strict"
	}
	switch arg {
	case "":
		m.pushChat("system", "Scope is currently "+current+". Use /scope strict | /scope expanded.")
		return m, nil
	case "strict", "lock", "lockdown":
		m.scope = "strict"
	case "expanded", "expand", "open", "broad", "wide":
		m.scope = "expanded"
	default:
		m.pushChat("system", "Usage: /scope [strict|expanded]")
		return m, nil
	}
	if m.exec != nil {
		m.exec.Scope = m.scope
	}
	if m.scope == "expanded" {
		m.pushChat("system", "Scope → expanded — file ops can touch ANY path on this machine. Use /scope strict to re-lock.")
	} else {
		m.pushChat("system", "Scope → strict — file ops sandboxed to "+m.cwd)
	}
	return m, nil
}

// cmdPanel toggles (or explicitly sets) the right-column activity panel.
// Usage: /panel            (toggle)
//        /panel hide|off   (force hidden)
//        /panel show|on    (force visible)
// When hidden the chat area reclaims the full terminal width — useful
// on narrow windows or when the panel's contents aren't interesting
// this session.
func cmdPanel(m *Model, args []string) (tea.Model, tea.Cmd) {
	arg := ""
	if len(args) > 0 {
		arg = strings.ToLower(args[0])
	}
	switch arg {
	case "hide", "off", "close", "0":
		m.panelHidden = true
	case "show", "on", "open", "1":
		m.panelHidden = false
	case "", "toggle":
		m.panelHidden = !m.panelHidden
	default:
		m.pushChat("system", "Usage: /panel [hide|show|toggle]")
		return m, nil
	}
	// Panel visibility changes the chat column width — force the render
	// cache to rebuild with the new innerW.
	m.historyDirty = true
	m.historyWidth = -1
	m.rerenderViewport()
	state := "visible"
	if m.panelHidden {
		state = "hidden"
	}
	m.pushChat("system", "Activity panel → "+state)
	return m, nil
}

// cmdDelegate exposes the executor's existing DelegationMode field as a
// slash command. Mirrors the Python /delegate at acorn/app.py:1137-1168.
//
//	/delegate                 show current mode + options
//	/delegate default|off|
//	         research|code|all set delegation mode
//	/delegate workers <n>     status-only — see note
//
// The five modes are already defined as constants in tools/executor.go
// (DelegateDefault / DelegateOff / DelegateResearch / DelegateCode /
// DelegateAll); checkDelegation() at executor.go:198 enforces them on
// every delegate_task call. We just route the mode change here.
//
// Broadcast on change: emits delegate:config so the companion app stays
// in sync (matches Python's bridge.broadcast at app.py:1147,1156).
//
// Workers note: Python's /delegate workers <n> sets a *client-side* cap
// on locally-spawned sub-agents. In Go, sub-agents run server-side on
// SPORE, so the equivalent is SPORE_MAX_SUBAGENT_CHILDREN env on the
// SPORE instance. We surface that as a status message rather than
// pretending to have the knob.
func cmdDelegate(m *Model, args []string) (tea.Model, tea.Cmd) {
	current := "default"
	if m.exec != nil && m.exec.Delegation != "" {
		current = string(m.exec.Delegation)
	}
	descs := map[string]string{
		"default":  "research+bg ok, orchestration stays local",
		"off":      "no delegation at all",
		"research": "only parallel web research",
		"code":     "research + parallel writes",
		"all":      "unrestricted",
	}
	if len(args) == 0 {
		var b strings.Builder
		b.WriteString("Delegation mode: " + current + "\n")
		for _, k := range []string{"default", "off", "research", "code", "all"} {
			b.WriteString("  /delegate " + k + " — " + descs[k] + "\n")
		}
		b.WriteString("  /delegate workers <n> — workers cap is server-side; see note")
		m.pushChat("system", strings.TrimRight(b.String(), "\n"))
		return m, nil
	}
	arg := strings.ToLower(args[0])
	if arg == "workers" {
		m.pushChat("system",
			"Workers cap is set server-side on SPORE (env SPORE_MAX_SUBAGENT_CHILDREN, default 8). "+
				"Edit the .env / spore.json on the SPORE instance you connect to.")
		return m, nil
	}
	if _, ok := descs[arg]; !ok {
		m.pushChat("system", "Usage: /delegate [default|off|research|code|all] (got: "+arg+")")
		return m, nil
	}
	if m.exec != nil {
		m.exec.Delegation = tools.DelegationMode(arg)
	}
	m.pushChat("system", "Delegation → "+arg+": "+descs[arg])
	// Broadcast to observers so companion app's /delegate UI stays in
	// sync. Mirrors acorn/app.py:1147,1156.
	m.Broadcast("delegate:config", map[string]any{"mode": arg})
	return m, nil
}

func init() {
	register(&slashCmd{
		Name:    "/context",
		Help:    "Show the project context block (add 'refresh' to re-send next turn)",
		Handler: cmdContext,
	})
	register(&slashCmd{
		Name:    "/tree",
		Help:    "Print the project file tree (optional depth, default 3)",
		Handler: cmdTree,
	})
	register(&slashCmd{
		Name:    "/init",
		Help:    "Create ACORN.md template + add .acorn/ to .gitignore",
		Handler: cmdInit,
	})
	register(&slashCmd{
		Name:    "/panel",
		Help:    "Toggle the right-column activity panel (hide|show|toggle)",
		Handler: cmdPanel,
	})
	register(&slashCmd{
		Name:    "/delegate",
		Help:    "Configure background delegation: default|off|research|code|all (or 'workers <n>')",
		Handler: cmdDelegate,
	})
	register(&slashCmd{
		Name:    "/scope",
		Help:    "Set file-op sandbox: strict (cwd only, default) or expanded (any path)",
		Handler: cmdScope,
	})
}
