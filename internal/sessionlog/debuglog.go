package sessionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DebugLogger writes a verbose diagnostic log for a single session to
// ~/.spore-code/logs/<ts>_<safe-id>.log. Port of acorn/session_log.py.
type DebugLogger struct {
	mu   sync.Mutex
	file *os.File
}

// OpenDebug starts a session debug log. Header mirrors Python's.
func OpenDebug(globalDir, sessionID, user, cwd string) *DebugLogger {
	dir := filepath.Join(globalDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	ts := time.Now().Format("20060102-150405")
	name := ts + "_" + truncate(safeID(sessionID), 60) + ".log"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	l := &DebugLogger{file: f}
	l.raw("=== Spore Code Session Log ===")
	l.raw("Session:  " + sessionID)
	l.raw("User:     " + user)
	l.raw("CWD:      " + cwd)
	l.raw("Started:  " + time.Now().Format(time.RFC3339))
	l.raw(fmt.Sprintf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH))
	l.raw(fmt.Sprintf("Runtime:  go %s", runtime.Version()))
	l.raw(fmt.Sprintf("PID:      %d", os.Getpid()))
	l.raw("===")
	l.raw("")
	return l
}

func (l *DebugLogger) raw(line string) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.WriteString(line + "\n")
}

func (l *DebugLogger) log(level, category, message string, extras ...any) {
	if l == nil || l.file == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] [%s] [%s] %s",
		time.Now().Format("15:04:05.000"), level, category, message)
	for i := 0; i+1 < len(extras); i += 2 {
		fmt.Fprintf(&b, " %v=%v", extras[i], truncateAny(extras[i+1], 200))
	}
	l.raw(b.String())
}

func (l *DebugLogger) Info(cat, msg string, kv ...any)  { l.log("info", cat, msg, kv...) }
func (l *DebugLogger) Warn(cat, msg string, kv ...any)  { l.log("warn", cat, msg, kv...) }
func (l *DebugLogger) Error(cat, msg string, kv ...any) { l.log("error", cat, msg, kv...) }
func (l *DebugLogger) Debug(cat, msg string, kv ...any) { l.log("debug", cat, msg, kv...) }

func (l *DebugLogger) Close() {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Close()
}

func truncateAny(v any, n int) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
