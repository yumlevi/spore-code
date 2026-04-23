package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/yumlevi/acorn-cli/go/internal/proto"
)

// BuildProjectContext returns the structured project metadata that gets
// sent on every chat:submit as a SIBLING to the message content. SPORE
// routes this into the system prompt instead of the message history,
// so the agent always knows the project state without paying for it
// in messages[] tokens every turn.
//
// This replaces the prose-blob version (GatherContext) for SPORE
// instances that advertise the projectContext capability. Old SPOREs
// still get GatherContext glued onto content as a fallback.
func BuildProjectContext(cwd, mode string) proto.ProjectContext {
	gitRoot := findGitRoot(cwd)
	project := filepath.Base(cwd)
	root := cwd
	if gitRoot != "" {
		project = filepath.Base(gitRoot)
		root = gitRoot
	}
	pc := proto.ProjectContext{
		Cwd:     cwd,
		Project: project,
		Mode:    mode,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	if gitRoot != "" {
		pc.GitBranch = gitBranch(cwd)
		pc.GitHash = gitOutput(gitRoot, "rev-parse", "--short", "HEAD")
		if status := gitOutput(gitRoot, "status", "--short"); status != "" {
			if len(status) > 1024 {
				status = status[:1024] + "\n…"
			}
			pc.GitStatus = status
		}
	}
	if pt := detectProjectType(gitRoot, cwd); pt != "" && pt != "Unknown" {
		pc.ProjectType = pt
	}
	if data, err := os.ReadFile(filepath.Join(root, "ACORN.md")); err == nil {
		s := string(data)
		if len(s) > 4096 {
			s = s[:4096]
		}
		pc.AcornMd = s
	}
	if tree := projectTreeList(root, 2, 100); len(tree) > 0 {
		pc.Tree = tree
	}
	if tools := detectToolsList(); len(tools) > 0 {
		pc.Tools = tools
	}
	return pc
}

// projectTreeList is projectTree's sibling for the structured-context
// path — returns the tree as a slice of paths instead of an ASCII-art
// string. Cheaper for SPORE to render however it wants and lets a
// future graph-side cache key on the path set.
func projectTreeList(root string, maxDepth, maxEntries int) []string {
	skip := map[string]struct{}{
		".git": {}, "node_modules": {}, ".venv": {}, "venv": {},
		"__pycache__": {}, "dist": {}, "build": {}, ".acorn": {},
		"target": {}, ".next": {}, ".cache": {},
	}
	var out []string
	var walk func(dir, rel string, depth int) bool
	walk = func(dir, rel string, depth int) bool {
		if depth > maxDepth || len(out) >= maxEntries {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if a.IsDir() != b.IsDir() {
				return a.IsDir()
			}
			return a.Name() < b.Name()
		})
		for _, e := range entries {
			if len(out) >= maxEntries {
				return false
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") && name != ".env" && name != ".gitignore" {
				continue
			}
			if _, drop := skip[name]; drop {
				continue
			}
			path := name
			if rel != "" {
				path = rel + "/" + name
			}
			if e.IsDir() {
				out = append(out, path+"/")
				if !walk(filepath.Join(dir, name), path, depth+1) {
					return false
				}
			} else {
				out = append(out, path)
			}
		}
		return true
	}
	walk(root, "", 1)
	return out
}

// detectToolsList — like detectTools but returns a slice instead of CSV.
func detectToolsList() []string {
	tools := []string{"node", "python3", "go", "rustc", "cargo", "bun", "deno", "docker", "git"}
	var present []string
	for _, t := range tools {
		if _, err := exec.LookPath(t); err == nil {
			present = append(present, t)
		}
	}
	return present
}

// GatherContext produces the first-message context block acorn/context.py
// injects before the user's initial prompt. This is a near-verbatim port
// of acorn/context.py:gather_context — same scope/sandbox/work-style
// instructions, same git/env/tree blocks, same ACORN.md inclusion.
//
// Why the parity matters: the agent on the SPORE server uses these
// blocks to decide which tools to use, where to write files, and how
// chatty to be. When the Go port sent only a stripped-down 5-line
// context, the agent had to guess at things Python's CLI was telling
// it explicitly (e.g. "ALL file ops are sandboxed to {cwd}", "don't
// use /workspace/ paths", "write incrementally not in a giant batch").
func GatherContext(cwd string) string {
	gitRoot := findGitRoot(cwd)
	project := filepath.Base(cwd)
	if gitRoot != "" {
		project = filepath.Base(gitRoot)
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("[Acorn Context — %s]", project))
	parts = append(parts, "CWD: "+cwd)
	parts = append(parts, fmt.Sprintf(
		"[SCOPE: You are working on the %q project at %s. "+
			"Focus only on this project. Do NOT reference, continue, or plan work from other projects "+
			"unless the user explicitly asks about them.]",
		project, cwd))
	parts = append(parts, fmt.Sprintf(
		"[CWD ENFORCEMENT: ALL file operations (read_file, write_file, edit_file, exec) "+
			"are sandboxed to %s. Paths outside %s will be REJECTED by the tool executor. "+
			"Do NOT use /workspace/ or any server-side path — those are inside the Anima container "+
			"and will be lost on restart. Write everything to %s on the user's machine.]",
		cwd, cwd, cwd))
	parts = append(parts,
		"[WORK STYLE: Work incrementally — one or two tool calls per turn, not six. "+
			"After each file write or command, briefly tell the user what you did and what's next. "+
			"Do NOT batch many write_file calls in a single response — the user can't see progress "+
			"and it takes too long to generate. Write one file, confirm, move to the next.\n"+
			"LONG COMMANDS: For commands that take a while (npm install, builds, large downloads), "+
			"run them with exec, then check output. Example workflow:\n"+
			"1. exec: npm install (with timeout)\n"+
			"2. Tell user: \"Installing deps, this may take a minute...\"\n"+
			"3. Check the result, report success/failure, then continue.\n"+
			"This keeps the user informed instead of going silent for 2 minutes.]")

	if gitRoot != "" {
		if branch := gitBranch(cwd); branch != "" {
			parts = append(parts, "Git: branch="+branch)
		} else {
			parts = append(parts, "Git: "+gitRoot)
		}
		if status := gitOutput(gitRoot, "status", "--short"); status != "" {
			lines := strings.Split(status, "\n")
			if len(lines) > 20 {
				status = strings.Join(lines[:20], "\n") + fmt.Sprintf("\n... (%d more)", len(lines)-20)
			}
			parts = append(parts, "Status:\n"+status)
		}
		if log := gitOutput(gitRoot, "log", "--oneline", "-5"); log != "" {
			parts = append(parts, "Recent commits:\n"+log)
		}
	}

	parts = append(parts, "Environment:\n"+gatherEnvironment())

	if pt := detectProjectType(gitRoot, cwd); pt != "" && pt != "Unknown" {
		parts = append(parts, "Detected project type: "+pt)
	}

	root := gitRoot
	if root == "" {
		root = cwd
	}
	if data, err := os.ReadFile(filepath.Join(root, "ACORN.md")); err == nil {
		s := string(data)
		if len(s) > 4000 {
			s = s[:4000]
		}
		parts = append(parts, "--- ACORN.md ---\n"+s+"\n--- end ---")
	}

	if tree := projectTree(root, 2, 50); tree != "" {
		parts = append(parts, "Project tree:\n"+tree)
	}

	return strings.Join(parts, "\n\n")
}

// gatherEnvironment is the rough equivalent of acorn/context.py's env
// summary — OS, arch, plus any well-known toolchains we can spot.
func gatherEnvironment() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("OS: %s/%s", runtime.GOOS, runtime.GOARCH))
	if files := topLevelClues(currentWorkingDir()); files != "" {
		lines = append(lines, "Project markers: "+files)
	}
	if tools := detectTools(); tools != "" {
		lines = append(lines, "Tools available: "+tools)
	}
	return strings.Join(lines, "\n")
}

// detectProjectType — simple heuristic mirroring Python's detect_project_type.
func detectProjectType(gitRoot, cwd string) string {
	root := gitRoot
	if root == "" {
		root = cwd
	}
	check := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case check("package.json"):
		if check("next.config.js") || check("next.config.ts") {
			return "Next.js"
		}
		if check("vite.config.ts") || check("vite.config.js") {
			return "Vite"
		}
		return "Node.js"
	case check("go.mod"):
		return "Go"
	case check("Cargo.toml"):
		return "Rust"
	case check("pyproject.toml"), check("requirements.txt"), check("setup.py"):
		return "Python"
	case check("Gemfile"):
		return "Ruby"
	case check("pom.xml"), check("build.gradle"):
		return "Java"
	case check("composer.json"):
		return "PHP"
	}
	return ""
}

// projectTree returns a depth-limited file tree similar to acorn/context.py:_tree.
// Skips hidden dirs and common cache/build trees.
func projectTree(root string, maxDepth, maxEntries int) string {
	skip := map[string]struct{}{
		".git": {}, "node_modules": {}, ".venv": {}, "venv": {},
		"__pycache__": {}, "dist": {}, "build": {}, ".acorn": {},
		"target": {}, ".next": {}, ".cache": {},
	}
	var b strings.Builder
	b.WriteString(filepath.Base(root) + "/\n")
	count := 0
	var walk func(dir, prefix string, depth int) bool
	walk = func(dir, prefix string, depth int) bool {
		if depth > maxDepth || count >= maxEntries {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		sort.SliceStable(entries, func(i, j int) bool {
			a, c := entries[i], entries[j]
			if a.IsDir() != c.IsDir() {
				return a.IsDir()
			}
			return a.Name() < c.Name()
		})
		n := len(entries)
		for i, e := range entries {
			if count >= maxEntries {
				b.WriteString(prefix + "└── …\n")
				return false
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") && name != ".env" && name != ".gitignore" {
				continue
			}
			if _, drop := skip[name]; drop {
				continue
			}
			isLast := i == n-1
			branch := "├── "
			next := prefix + "│   "
			if isLast {
				branch = "└── "
				next = prefix + "    "
			}
			b.WriteString(prefix + branch + name + "\n")
			count++
			if e.IsDir() {
				if !walk(filepath.Join(dir, name), next, depth+1) {
					return false
				}
			}
		}
		return true
	}
	walk(root, "", 1)
	return b.String()
}

// gitOutput runs a git subcommand in dir and returns trimmed stdout, or "".
func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func currentWorkingDir() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func topLevelClues(cwd string) string {
	clues := []string{
		"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt",
		"pom.xml", "build.gradle", "Gemfile", "composer.json",
		"Makefile", "README.md", "tsconfig.json", "vite.config.ts",
	}
	var found []string
	for _, c := range clues {
		if _, err := os.Stat(filepath.Join(cwd, c)); err == nil {
			found = append(found, c)
		}
	}
	return strings.Join(found, ", ")
}

func detectTools() string {
	tools := []string{"node", "python3", "go", "rustc", "cargo", "bun", "deno", "docker", "git"}
	var present []string
	for _, t := range tools {
		if _, err := exec.LookPath(t); err == nil {
			present = append(present, t)
		}
	}
	return strings.Join(present, ", ")
}
