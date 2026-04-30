package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// findGitRoot returns the git toplevel for cwd, or "" if not a git repo.
func findGitRoot(cwd string) string {
	out, err := execOutput(cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// FindGitRoot is the exported form used by main.go for the -c picker.
func FindGitRoot(cwd string) string { return findGitRoot(cwd) }

// gitBranch returns the current git branch name, or "".
func gitBranch(cwd string) string {
	out, _ := execOutput(cwd, "git", "branch", "--show-current")
	return strings.TrimSpace(out)
}

// ComputeSessionID returns a STABLE session id for a (user, cwd) pair:
//
//	cli:<user>@<project-name>-<pathhash>
//
// Stable means every acorn invocation in the same project lands on the
// same server-side session, picking up prior turns automatically. The
// /new slash command is the explicit "start fresh" escape hatch.
//
// History — the Python port (and earlier Go versions) baked the launch
// timestamp into the id so every run was fresh, relying on `-c` /
// load_last_session for resume. For coding work that's the wrong
// default: relaunching the binary in the same project created a new
// server-side session every time, losing the agent's recollection
// of the conversation you were still visibly reading in the UI.
// Combined with SPORE's 60-minute idle-timeout sweep (now also
// cli-aware, see anima-new/src/agent/sessions.js), sessions were
// getting silently reset under the user.
func ComputeSessionID(user, cwd string) string {
	root := findGitRoot(cwd)
	if root == "" {
		root = cwd
	}
	name := filepath.Base(root)
	h := sha256.Sum256([]byte(root))
	pathHash := hex.EncodeToString(h[:])[:8]
	return fmt.Sprintf("cli:%s@%s-%s", user, name, pathHash)
}

// ComputeSessionIDFresh returns a UNIQUE session id for a (user, cwd)
// pair, suffixed with a UTC timestamp:
//
//	cli:<user>@<project-name>-<pathhash>-<YYYYMMDDTHHMMSS>
//
// Used for the no-flag launch path (when AutoResume is off) and for the
// /new slash command. Old sessions are still discoverable via /sessions
// or `acorn -c`'s picker.
func ComputeSessionIDFresh(user, cwd string) string {
	return ComputeSessionID(user, cwd) + "-" + time.Now().UTC().Format("20060102T150405")
}

// execOutput runs a command and returns stdout. Convenience wrapper.
func execOutput(cwd, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = cwd
	out, err := c.Output()
	return string(out), err
}
