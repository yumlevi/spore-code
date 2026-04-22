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

// projectName mirrors acorn/session.py:project_name — git root basename or cwd basename.
func projectName(cwd string) string {
	root := findGitRoot(cwd)
	if root == "" {
		root = cwd
	}
	return filepath.Base(root)
}

// ComputeSessionID mirrors acorn/session.py:compute_session_id.
//
//	cli:<user>@<project-name>-<pathhash>-<tshex>
//
// The timestamp ensures every invocation gets a fresh session (Python
// behaviour — resumes use -c / load_last_session to reuse).
func ComputeSessionID(user, cwd string) string {
	root := findGitRoot(cwd)
	if root == "" {
		root = cwd
	}
	name := filepath.Base(root)
	h := sha256.Sum256([]byte(root))
	pathHash := hex.EncodeToString(h[:])[:8]
	ts := fmt.Sprintf("%x", time.Now().Unix())
	return fmt.Sprintf("cli:%s@%s-%s-%s", user, name, pathHash, ts)
}

// execOutput runs a command and returns stdout. Convenience wrapper.
func execOutput(cwd, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = cwd
	out, err := c.Output()
	return string(out), err
}
