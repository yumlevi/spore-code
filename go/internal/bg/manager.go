// Package bg manages long-running background processes spawned by the agent
// (or user). Port of acorn/background.py (scaled down).
package bg

import (
	"bufio"
	"container/list"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Process is a single tracked background subprocess.
type Process struct {
	ID       int
	Command  string
	cwd      string
	cmd      *exec.Cmd
	Started  time.Time
	Ended    time.Time
	ExitCode int
	LogPath  string
	Running  bool

	mu     sync.Mutex
	output *list.List // last 500 lines
}

// Manager holds all currently-tracked background processes.
type Manager struct {
	mu     sync.Mutex
	next   int32
	procs  map[int]*Process
	logDir string
}

// New creates a manager. logDir becomes the root for per-process log files.
func New(logDir string) *Manager {
	_ = os.MkdirAll(logDir, 0o755)
	return &Manager{
		procs:  make(map[int]*Process),
		logDir: logDir,
	}
}

// Launch starts a command in the background. Returns the Process handle.
// The caller can later use ID for /bg read and /bg kill.
func (m *Manager) Launch(command, cwd string) (*Process, error) {
	id := int(atomic.AddInt32(&m.next, 1))
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.Command(shell, flag, command)
	cmd.Dir = cwd
	// Tie the child's lifetime to ours — Linux PDEATHSIG / Windows
	// JobObject KillOnJobClose. So a kill -9 on acorn doesn't leak
	// background processes.
	applyChildLifetime(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // merge

	logPath := filepath.Join(m.logDir, fmt.Sprintf("bg-%d-%s.log", id, time.Now().Format("20060102-150405")))
	logF, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return nil, err
	}
	// Windows: assign to JobObject after start so it gets killed when we die.
	_ = attachAndResume(cmd)

	p := &Process{
		ID:       id,
		Command:  command,
		cwd:      cwd,
		cmd:      cmd,
		Started:  time.Now(),
		LogPath:  logPath,
		Running:  true,
		output:   list.New(),
	}

	m.mu.Lock()
	m.procs[id] = p
	m.mu.Unlock()

	// Output pump: capture last 500 lines + tee to log file.
	go func() {
		defer func() { _ = logF.Close() }()
		r := bufio.NewScanner(stdout)
		r.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for r.Scan() {
			line := r.Text()
			p.appendOutput(line)
			fmt.Fprintln(logF, line)
		}
	}()
	// Waiter: mark dead, capture exit code.
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.Running = false
		p.Ended = time.Now()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				p.ExitCode = ee.ExitCode()
			} else {
				p.ExitCode = -1
			}
		}
		p.mu.Unlock()
	}()

	return p, nil
}

// Kill terminates a process by id.
func (m *Manager) Kill(id int) bool {
	m.mu.Lock()
	p, ok := m.procs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	if p.cmd.Process == nil || !p.Running {
		return false
	}
	_ = p.cmd.Process.Kill()
	return true
}

// Get returns a process by id.
func (m *Manager) Get(id int) *Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[id]
}

// List returns every tracked process (running + finished).
func (m *Manager) List() []*Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Process, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, p)
	}
	return out
}

// KillAll terminates every tracked running process. Called on program exit.
func (m *Manager) KillAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		if p.Running && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}
}

// Output snapshot (last 500 lines).
func (p *Process) Output() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, p.output.Len())
	for e := p.output.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(string))
	}
	return out
}

func (p *Process) appendOutput(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.output.PushBack(line)
	if p.output.Len() > 500 {
		p.output.Remove(p.output.Front())
	}
}

// Elapsed returns the human-readable runtime duration.
func (p *Process) Elapsed() string {
	end := p.Ended
	if end.IsZero() {
		end = time.Now()
	}
	secs := int(end.Sub(p.Started).Seconds())
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	default:
		return fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60)
	}
}

var _ = io.EOF // keep io import if future refactor needs it
