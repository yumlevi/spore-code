package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/acorn-cli/internal/codeindex"
	"github.com/yumlevi/acorn-cli/internal/conn"
	"github.com/yumlevi/acorn-cli/internal/tools"
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
	// /help — registry-backed so any command added via register() shows
	// up automatically. The static fallback in update.go exists for
	// older callers but the registered version takes priority via
	// dispatchSlash.
	register(&slashCmd{
		Name:    "/help",
		Help:    "Show the full list of slash commands and what each does",
		Handler: cmdHelp,
	})
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

	// codeindex slash commands — thin wrappers over the local tools
	// dispatched by Executor.Execute. All are read/index-only against
	// .acorn/index.db.
	register(&slashCmd{
		Name:    "/index",
		Help:    "Build the per-project code index (.acorn/index.db). 'force' re-indexes everything.",
		Handler: cmdIndex,
	})
	register(&slashCmd{
		Name:    "/architecture",
		Aliases: []string{"/arch"},
		Help:    "Show clusters, entry points, hot paths, tech stack from the code index",
		Handler: cmdArchitecture,
	})
	register(&slashCmd{
		Name:    "/why",
		Help:    "/why <symbol> — show callers (depth 3) of a symbol from the index",
		Handler: cmdWhy,
	})
	register(&slashCmd{
		Name:    "/calls",
		Help:    "/calls <symbol> — show callees (depth 3) of a symbol from the index",
		Handler: cmdCalls,
	})
	register(&slashCmd{
		Name:    "/impact",
		Help:    "Show transitive caller blast-radius for current `git diff` paths",
		Handler: cmdImpact,
	})
	register(&slashCmd{
		Name:    "/scripts",
		Help:    "/scripts → list saved project scripts. /scripts <name> → fetch one.",
		Handler: cmdScripts,
	})
}

// ── codeindex commands ─────────────────────────────────────────────
//
// Each command runs the underlying tool inside a tea.Cmd goroutine so
// the Bubble Tea Update loop never blocks. Indexing a real project can
// take seconds; running it on the UI thread freezes the entire TUI for
// that duration. The Cmd posts a CodeindexResultMsg back; update.go
// renders it into the chat scrollback.

// CodeindexResultMsg is the deferred result from any codeindex slash
// command. Label is the chat header (e.g. "/index" or "/why Foo");
// Result is whatever the tool returned (typically map[string]any).
type CodeindexResultMsg struct {
	Label  string
	Result any
}

// CodeindexProgressMsg streams in periodically while /index runs so
// big repos don't look like a hang. update.go appends one chat line
// per message; the indexer throttles emission to ~once every 2s.
type CodeindexProgressMsg struct {
	FilesScanned int
	FilesParsed  int
	FilesSkipped int
	Symbols      int
	Calls        int
	ElapsedMs    int
	Note         string
}

// runCodeindexAsync wraps a tool call in a tea.Cmd. Captures cwd up-
// front so a /scope or /cwd change mid-flight doesn't retarget.
func runCodeindexAsync(label, cwd string, fn func(string) any) tea.Cmd {
	return func() tea.Msg {
		return CodeindexResultMsg{Label: label, Result: fn(cwd)}
	}
}

// autoMirrorCodeGraph computes the architecture summary from the
// freshly-updated .acorn/index.db and pushes it to the SPORE-side
// acorn-cli plugin via a `code_graph:summary` WS frame. The plugin
// writes the summary onto the project node's `code_graph` aspect,
// so subsequent sessions on this project (or fresh laptops, or
// session distillation) see the codebase shape without anyone
// having to call architecture + update_code_graph_summary by hand.
//
// Best-effort — every failure path silently no-ops since this is a
// background nicety, not a load-bearing part of the index flow.
func autoMirrorCodeGraph(client *conn.Client, cwd, sessionID, userName string) {
	if client == nil || cwd == "" {
		return
	}
	store, err := codeindex.Open(cwd)
	if err != nil {
		return
	}
	defer store.Close()
	arch, err := codeindex.ComputeArchitecture(store)
	if err != nil || arch == nil {
		return
	}
	// Marshal-friendly payload: convert the concrete typed slices to
	// []map[string]any so JSON encoding produces stable shapes the
	// plugin's projects.upsertProjectCodeGraph can iterate.
	techStack := make([]map[string]any, 0, len(arch.TechStack))
	for _, t := range arch.TechStack {
		techStack = append(techStack, map[string]any{"language": t.Language, "files": t.Files, "symbols": t.Symbols})
	}
	clusters := make([]map[string]any, 0, len(arch.Clusters))
	for _, c := range arch.Clusters {
		clusters = append(clusters, map[string]any{"name": c.Name, "path": c.Path, "files": c.Files, "symbols": c.Symbols, "dominant_lang": c.DominantLang})
	}
	entryPoints := make([]map[string]any, 0, len(arch.EntryPoints))
	for _, e := range arch.EntryPoints {
		entryPoints = append(entryPoints, map[string]any{"qname": e.QName, "name": e.Name, "file": e.File, "line": e.Line, "kind": e.Kind, "language": e.Language})
	}
	hotPaths := make([]map[string]any, 0, len(arch.HotPaths))
	for _, h := range arch.HotPaths {
		hotPaths = append(hotPaths, map[string]any{"qname": h.QName, "name": h.Name, "file": h.File, "line": h.Line, "callers": h.Callers, "language": h.Language})
	}
	stats := map[string]any{
		"files": arch.Stats.Files, "symbols": arch.Stats.Symbols,
		"functions": arch.Stats.Functions, "methods": arch.Stats.Methods,
		"classes": arch.Stats.Classes, "calls": arch.Stats.Calls,
	}
	summary := map[string]any{
		"index_head":   arch.IndexHead,
		"stats":        stats,
		"tech_stack":   techStack,
		"entry_points": entryPoints,
		"clusters":     clusters,
		"hot_paths":    hotPaths,
		"notes":        arch.Notes,
	}
	_ = client.Send(map[string]any{
		"type":      "code_graph:summary",
		"sessionId": sessionID,
		"userName":  userName,
		"cwd":       cwd,
		"summary":   summary,
	})
}

func cmdIndex(m *Model, args []string) (tea.Model, tea.Cmd) {
	input := map[string]any{}
	if len(args) > 0 && args[0] == "force" {
		input["force"] = true
	}
	m.pushChat("system", "Indexing codebase… (this may take a few seconds — progress every 2s below)")
	send := m.sendProgramMsg
	cwd := m.cwd
	return m, func() tea.Msg {
		var onProgress tools.IndexProgressFn
		if send != nil {
			onProgress = func(p tools.IndexProgress) {
				send(CodeindexProgressMsg{
					FilesScanned: p.FilesScanned, FilesParsed: p.FilesParsed,
					FilesSkipped: p.FilesSkipped, Symbols: p.Symbols, Calls: p.Calls,
					ElapsedMs: p.ElapsedMs, Note: p.Note,
				})
			}
		}
		r := tools.IndexCodebaseWithProgress(input, cwd, onProgress)
		return CodeindexResultMsg{Label: "/index", Result: r}
	}
}

func cmdArchitecture(m *Model, args []string) (tea.Model, tea.Cmd) {
	m.pushChat("system", "Computing architecture summary…")
	return m, runCodeindexAsync("/architecture", m.cwd, func(cwd string) any {
		return tools.Architecture(map[string]any{}, cwd)
	})
}

func cmdWhy(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.pushChat("system", "usage: /why <symbol-name>")
		return m, nil
	}
	name := args[0]
	m.pushChat("system", "Tracing callers of "+name+"…")
	return m, runCodeindexAsync("/why "+name, m.cwd, func(cwd string) any {
		return tools.TraceCalls(map[string]any{
			"name":      name,
			"direction": "callers",
			"depth":     3,
		}, cwd)
	})
}

func cmdCalls(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.pushChat("system", "usage: /calls <symbol-name>")
		return m, nil
	}
	name := args[0]
	m.pushChat("system", "Tracing callees of "+name+"…")
	return m, runCodeindexAsync("/calls "+name, m.cwd, func(cwd string) any {
		return tools.TraceCalls(map[string]any{
			"name":      name,
			"direction": "callees",
			"depth":     3,
		}, cwd)
	})
}

func cmdImpact(m *Model, args []string) (tea.Model, tea.Cmd) {
	m.pushChat("system", "Computing change impact for staged + unstaged paths…")
	return m, runCodeindexAsync("/impact", m.cwd, func(cwd string) any {
		return tools.Impact(map[string]any{}, cwd)
	})
}

// cmdScripts prints a one-line hint that nudges the user toward asking
// the agent. The actual scripts memory lives server-side in the SPORE
// graph (plugins/session-graph/lib/scripts.js); the agent calls
// list_project_scripts / get_project_script / save_project_script /
// record_script_outcome on the user's behalf. Keeping /scripts as a
// discoverable reminder is more useful than wiring a synthetic
// chat-submit pathway from the CLI.
func cmdScripts(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.pushChat("system",
			"Saved project scripts live in the graph (server-side). Ask the agent: "+
				"\"list saved project scripts for this project\" — it will call list_project_scripts. "+
				"To fetch one: \"get the saved script <name>\" → get_project_script. "+
				"To save one: ask the agent to save it via save_project_script.")
		return m, nil
	}
	m.pushChat("system",
		fmt.Sprintf("Ask the agent: \"fetch the saved project script %q\" — it will call get_project_script.", args[0]))
	return m, nil
}

// renderCodeindexResult turns a tool's return map into chat-friendly
// markdown. Each codeindex tool returns the same envelope shape
// ({ok, ...}); we shape the body per known label so the agent (and
// the user) gets a readable summary instead of a Go-struct dump.
func renderCodeindexResult(label string, r any) string {
	if r == nil {
		return label + " — (no result)"
	}
	m, ok := r.(map[string]any)
	if !ok {
		return label + ":\n" + fmt.Sprintf("%+v", r)
	}
	if okFlag, present := m["ok"]; present && okFlag != true {
		errMsg := ""
		if s, ok := m["error"].(string); ok {
			errMsg = s
		} else if s, ok := m["reason"].(string); ok {
			errMsg = s
		}
		if errMsg == "" {
			errMsg = "tool returned ok=false"
		}
		return label + " — failed: " + errMsg
	}
	switch {
	case strings.HasPrefix(label, "/index"):
		return renderIndexResult(label, m)
	case strings.HasPrefix(label, "/architecture"):
		return renderArchitectureResult(label, m)
	case strings.HasPrefix(label, "/why") || strings.HasPrefix(label, "/calls"):
		return renderTraceCallsResult(label, m)
	case strings.HasPrefix(label, "/impact"):
		return renderImpactResult(label, m)
	}
	// Fallback: compact key=value listing (excluding ok flag).
	return label + ":\n" + compactKV(m)
}

func renderIndexResult(label string, m map[string]any) string {
	took := readInt(m, "took_ms")
	files := readInt(m, "files")
	skipped := readInt(m, "skipped")
	unsupported := readInt(m, "unsupported")
	syms := readInt(m, "symbols")
	calls := readInt(m, "calls")
	totalFiles := readInt(m, "total_files")
	totalSyms := readInt(m, "total_syms")
	head, _ := m["index_head"].(string)
	var b strings.Builder
	fmt.Fprintf(&b, "%s — done in %dms\n", label, took)
	fmt.Fprintf(&b, "  parsed: %d files (%d skipped unchanged), %d symbols, %d call edges\n", files, skipped, syms, calls)
	if unsupported > 0 {
		// Surface the gap loud and clear so 0-symbol results don't
		// look like a bug. Today: only Go and TS/JS have extractors.
		fmt.Fprintf(&b, "  unsupported: %d files (extractor not yet built", unsupported)
		if ub, ok := m["unsupported_by_lang"].(map[string]int); ok && len(ub) > 0 {
			parts := make([]string, 0, len(ub))
			for l, n := range ub {
				parts = append(parts, fmt.Sprintf("%s=%d", l, n))
			}
			sort.Strings(parts)
			fmt.Fprintf(&b, "; %s)\n", strings.Join(parts, ", "))
		} else {
			b.WriteString(")\n")
		}
	}
	if totalFiles > 0 || totalSyms > 0 {
		fmt.Fprintf(&b, "  index now: %d files, %d symbols\n", totalFiles, totalSyms)
	}
	if head != "" {
		fmt.Fprintf(&b, "  index_head: %s\n", head)
	}
	if byLang, ok := m["by_language"].(map[string]int); ok && len(byLang) > 0 {
		var langs []string
		for l, n := range byLang {
			langs = append(langs, fmt.Sprintf("%s=%d", l, n))
		}
		sort.Strings(langs)
		fmt.Fprintf(&b, "  by language: %s\n", strings.Join(langs, ", "))
	}
	if files == 0 && unsupported == 0 {
		// Nothing got walked at all — most likely the cwd has no
		// recognized source file extensions, or everything's behind a
		// noise-dir filter (.git, node_modules, .venv, dist, build,
		// target, etc.) Surface a hint so the user can self-diagnose.
		b.WriteString("  No supported source files found in this directory.\n")
		b.WriteString("  Recognized: .go, .ts/.tsx/.mts/.cts, .js/.jsx/.mjs/.cjs, .py, .rs.\n")
		b.WriteString("  Check that you're in the project root, or override with index_codebase({roots:[\"src\"]}).\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderArchitectureResult(label string, m map[string]any) string {
	var b strings.Builder
	b.WriteString(label + ":\n")

	if stats, ok := m["stats"].(any); ok {
		// stats is the ArchitectureStats struct passed through as-is;
		// a fmt.Sprintf("%+v") on it is the cheapest readable form.
		fmt.Fprintf(&b, "  stats: %+v\n", stats)
	}
	if head, _ := m["index_head"].(string); head != "" {
		fmt.Fprintf(&b, "  index_head: %s\n", head)
	}

	// Tech stack — the value is []codeindex.LanguageBreakdown but we
	// only need the language and file count for a one-line render.
	if ts := m["tech_stack"]; ts != nil {
		fmt.Fprintf(&b, "  tech_stack: %s\n", oneLineSlice(ts))
	}
	if eps := m["entry_points"]; eps != nil {
		fmt.Fprintf(&b, "  entry_points: %s\n", oneLineSlice(eps))
	}
	if cls := m["clusters"]; cls != nil {
		fmt.Fprintf(&b, "  clusters: %s\n", oneLineSlice(cls))
	}
	if hp := m["hot_paths"]; hp != nil {
		fmt.Fprintf(&b, "  hot_paths: %s\n", oneLineSlice(hp))
	}
	if notes, ok := m["notes"].([]string); ok && len(notes) > 0 {
		b.WriteString("  notes:\n")
		for _, n := range notes {
			b.WriteString("    - " + n + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTraceCallsResult(label string, m map[string]any) string {
	count := readInt(m, "count")
	if count == 0 {
		return label + " — 0 edges (try a different name or `force`-reindex first)"
	}
	dir, _ := m["direction"].(string)
	depth := readInt(m, "depth")
	truncated, _ := m["truncated"].(bool)
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d edges (direction=%s depth=%d%s):\n", label, count, dir, depth, ternary(truncated, ", TRUNCATED", ""))
	// Print up to 25 edges; agent gets the full list via the JSON tool result.
	if edges, ok := m["edges"].([]struct{ /* anonymous from inline closure */ }); ok {
		_ = edges
	}
	// Use reflection-light loop via type assertion onto []any.
	if edges, ok := m["edges"].([]any); ok {
		shown := 0
		for _, e := range edges {
			if shown >= 25 {
				fmt.Fprintf(&b, "  … (%d more)\n", count-shown)
				break
			}
			fmt.Fprintf(&b, "  %v\n", e)
			shown++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderImpactResult(label string, m map[string]any) string {
	totalCallers := readInt(m, "total_callers")
	depth := readInt(m, "depth")
	paths, _ := m["paths"].([]string)
	syms, _ := m["affected_symbols"].([]map[string]any)
	if note, ok := m["note"].(string); ok && len(paths) == 0 {
		return label + " — " + note
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d affected symbols, %d transitive callers (depth=%d) across %d paths\n",
		label, len(syms), totalCallers, depth, len(paths))
	for i, s := range syms {
		if i >= 15 {
			fmt.Fprintf(&b, "  … (%d more)\n", len(syms)-i)
			break
		}
		fmt.Fprintf(&b, "  %v::%v  callers=%v  @%v:%v\n",
			s["file"], s["name"], s["transitive_callers"], s["file"], s["line"])
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── tiny helpers ───────────────────────────────────────────────────

func readInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func compactKV(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "ok" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %v\n", k, m[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// oneLineSlice formats a slice into a compact "[a, b, c…]" form. Falls
// back to %+v when the type isn't a simple slice. Caps at 5 elements.
func oneLineSlice(v any) string {
	switch s := v.(type) {
	case []any:
		return formatAnySlice(s, 5)
	case []map[string]any:
		out := make([]any, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return formatAnySlice(out, 5)
	}
	str := fmt.Sprintf("%+v", v)
	if len(str) > 200 {
		str = str[:200] + "…"
	}
	return str
}

func formatAnySlice(s []any, max int) string {
	if len(s) == 0 {
		return "[]"
	}
	parts := make([]string, 0, max)
	for i, e := range s {
		if i >= max {
			break
		}
		parts = append(parts, fmt.Sprintf("%v", e))
	}
	suffix := ""
	if len(s) > max {
		suffix = fmt.Sprintf(" … (%d more)", len(s)-max)
	}
	return "[" + strings.Join(parts, ", ") + suffix + "]"
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
