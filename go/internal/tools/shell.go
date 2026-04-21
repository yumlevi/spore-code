package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yumlevi/acorn-cli/go/internal/bg"
)

// dangerousPatterns — exact substrings we refuse to run. Mirror
// acorn/tools/shell.py:DANGEROUS_PATTERNS.
var dangerousPatterns = []string{
	"rm -rf /",
	"mkfs",
	"> /dev/sd",
	":(){:|:&};:",
	"chmod -R 777 /",
}

// blockedPaths — sensitive file paths that any command reference is enough
// to refuse execution.
var blockedPaths = []string{
	"/etc/shadow", "/etc/passwd-", "/etc/sudoers",
	"~/.ssh/id_", "~/.ssh/authorized_keys",
	"~/.gnupg", "~/.aws/credentials", "~/.kube/config",
}

// backgroundHints — commands likely to run forever; the Python version
// auto-backgrounds these. Go port doesn't yet have a process manager, so
// we just refuse them with a useful error.
var backgroundHints = []string{
	"npm start", "npm run dev", "npm run serve", "yarn start", "yarn dev",
	"python -m http.server", "python manage.py runserver",
	"node server", "nodemon", "next dev", "vite",
	"flask run", "uvicorn", "gunicorn", "cargo run",
	"docker compose up", "docker-compose up",
	"tail -f", "watch ",
}

var blockedPathRe = buildBlockedPathsRe()

func buildBlockedPathsRe() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, 2*len(blockedPaths))
	for _, p := range blockedPaths {
		out = append(out, regexp.MustCompile(regexp.QuoteMeta(p)))
		if strings.HasPrefix(p, "~") {
			home, _ := os.UserHomeDir()
			if home != "" {
				expanded := strings.Replace(p, "~", home, 1)
				out = append(out, regexp.MustCompile(regexp.QuoteMeta(expanded)))
			}
		}
	}
	return out
}

func checkPathSafety(cmd string) string {
	for _, r := range blockedPathRe {
		if r.FindStringIndex(cmd) != nil {
			return "Command references sensitive path: " + r.String()
		}
	}
	return ""
}

// Exec implements the exec tool. Input: command, timeout (ms), background.
// pm: optional background manager. If provided and the command is marked
// background OR matches a known-server-like pattern, launch detached and
// return a process handle instead of waiting.
// on: optional per-line output callback (used by the UI to stream exec
// output live during the call).
func Exec(input map[string]any, cwd string, logDir string, pm *bg.Manager, on func(line string)) any {
	command := asString(input["command"], "")
	if command == "" {
		return map[string]string{"error": "command is required"}
	}

	// Intercept /bg subcommands embedded in exec (agent does this to read
	// background process output via the tool layer).
	if strings.HasPrefix(strings.TrimSpace(command), "/bg") {
		return handleBgSubcommand(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), "/bg")), pm)
	}

	for _, p := range dangerousPatterns {
		if strings.Contains(command, p) {
			return map[string]string{"error": "Blocked dangerous command pattern: " + p}
		}
	}
	if err := checkPathSafety(command); err != "" {
		return map[string]string{"error": err}
	}
	if pm != nil && (asBool(input["background"], false) || isServerLike(command)) {
		p, err := pm.Launch(command, cwd)
		if err != nil {
			return map[string]string{"error": "background launch failed: " + err.Error()}
		}
		// Brief wait for early-crash detection.
		time.Sleep(2 * time.Second)
		early := strings.Join(p.Output(), "\n")
		if !p.Running {
			return map[string]any{
				"output":   early,
				"exitCode": p.ExitCode,
				"note":     fmt.Sprintf("Process exited (exit %d)", p.ExitCode),
				"logFile":  p.LogPath,
			}
		}
		if len(early) > 4000 {
			early = early[:4000]
		}
		return map[string]any{
			"output":      early,
			"backgrounded": true,
			"processId":   p.ID,
			"logFile":     p.LogPath,
			"note": fmt.Sprintf("Running in background as #%d. Check output via `exec /bg %d`. Kill: `exec /bg kill %d`.",
				p.ID, p.ID, p.ID),
		}
	}

	timeoutMs := asInt(input["timeout"], 120000)
	if timeoutMs > 600000 {
		timeoutMs = 600000
	}
	inactivity := time.Duration(timeoutMs) * time.Millisecond

	// Log file setup — .acorn/logs/<ts>.log.
	var logPath string
	var logW *os.File
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err == nil {
			logPath = filepath.Join(logDir, fmt.Sprintf("exec-%s.log", time.Now().Format("20060102-150405")))
			if f, err := os.Create(logPath); err == nil {
				fmt.Fprintf(f, "# Command: %s\n# Time: %s\n\n", command, time.Now().Format(time.RFC3339))
				logW = f
			}
		}
	}
	defer func() {
		if logW != nil {
			_ = logW.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.CommandContext(ctx, shell, flag, command)
	cmd.Dir = cwd
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	cmd.Stderr = cmd.Stdout // merge
	stdoutPipe, _ = cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return map[string]string{"error": err.Error()}
	}

	start := time.Now()

	// Read lines with inactivity timeout.
	var (
		mu          sync.Mutex
		lines       []string
		lineCh      = make(chan string, 1024)
		done        = make(chan error, 1)
		timedOut    bool
		totalBytes  int
	)
	go func() {
		sc := bufio.NewScanner(stdoutPipe)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			l := sc.Text()
			lineCh <- l
		}
		close(lineCh)
	}()
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(inactivity)
	defer timer.Stop()

collect:
	for {
		select {
		case l, ok := <-lineCh:
			if !ok {
				// stdout EOF — wait for process exit.
				break collect
			}
			mu.Lock()
			lines = append(lines, l)
			totalBytes += len(l) + 1
			mu.Unlock()
			if logW != nil {
				fmt.Fprintln(logW, l)
			}
			if on != nil {
				on(l)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(inactivity)
		case <-timer.C:
			timedOut = true
			_ = cmd.Process.Kill()
			break collect
		}
	}
	err = <-done
	duration := time.Since(start).Milliseconds()

	mu.Lock()
	raw := strings.Join(lines, "\n")
	mu.Unlock()

	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	if logW != nil {
		fmt.Fprintf(logW, "\n# Exit: %d, Duration: %dms\n", exitCode, duration)
	}

	if timedOut {
		return map[string]any{
			"error":    fmt.Sprintf("Command timed out after %dms of inactivity", inactivity.Milliseconds()),
			"exitCode": -1,
			"logFile":  logPath,
		}
	}

	output := raw
	truncated := false
	if len(output) > 8000 {
		mid := len(output) - 8000
		output = output[:4000] + fmt.Sprintf("\n\n[... %d chars truncated ...]\n\n", mid) + output[len(output)-4000:]
		truncated = true
	}
	result := map[string]any{
		"output":   output,
		"exitCode": exitCode,
	}
	if logPath != "" {
		result["logFile"] = logPath
	}
	if truncated && logPath != "" {
		result["note"] = fmt.Sprintf("Output truncated (%d chars). Full output: %s", len(raw), logPath)
	}
	return result
}

func isServerLike(cmd string) bool {
	for _, h := range backgroundHints {
		if strings.Contains(cmd, h) {
			return true
		}
	}
	return false
}

// handleBgSubcommand mirrors shell.py:_handle_bg_command — agent can send
// `exec /bg` / `/bg list` / `/bg <id>` / `/bg kill <id>` to inspect or
// manage background processes via the normal tool channel.
func handleBgSubcommand(args string, pm *bg.Manager) any {
	if pm == nil {
		return map[string]string{"error": "Background manager not available"}
	}
	if args == "" || args == "list" {
		procs := pm.List()
		if len(procs) == 0 {
			return map[string]string{"output": "No background processes"}
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
			lines = append(lines, fmt.Sprintf("#%d  %s  %s  %s", p.ID, st, p.Elapsed(), cmd))
		}
		return map[string]any{"output": strings.Join(lines, "\n")}
	}
	parts := strings.SplitN(args, " ", 2)
	sub := parts[0]
	if sub == "kill" && len(parts) > 1 {
		id, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return map[string]string{"error": "Invalid process ID: " + parts[1]}
		}
		if pm.Kill(id) {
			return map[string]any{"output": fmt.Sprintf("Killed #%d", id)}
		}
		return map[string]string{"error": fmt.Sprintf("Process #%d not found or already stopped", id)}
	}
	// Default: treat as id to read last output.
	id, err := strconv.Atoi(sub)
	if err != nil {
		return map[string]string{"error": "Invalid /bg subcommand: " + sub}
	}
	p := pm.Get(id)
	if p == nil {
		return map[string]string{"error": fmt.Sprintf("Process #%d not found", id)}
	}
	out := strings.Join(p.Output(), "\n")
	if len(out) > 8000 {
		out = out[len(out)-8000:]
	}
	st := "running"
	if !p.Running {
		st = fmt.Sprintf("exited (%d)", p.ExitCode)
	}
	return map[string]any{
		"output":   out,
		"running":  p.Running,
		"logFile":  p.LogPath,
		"status":   st,
		"elapsed":  p.Elapsed(),
	}
}
