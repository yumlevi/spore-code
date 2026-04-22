//go:build !windows

package bg

import (
	"os/exec"
	"syscall"
)

// applyChildLifetime makes the child die when acorn dies, even on
// SIGKILL. Mirrors acorn/background.py:_unix_preexec which uses
// PR_SET_PDEATHSIG via libc.prctl. Linux-only kernel feature.
func applyChildLifetime(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Pdeathsig is Linux-specific. On other Unixes (macOS, BSD) the
	// field doesn't exist on syscall.SysProcAttr — guarded by build tag
	// + reflection-style assignment. Compile-time check below.
	setPdeathsig(cmd.SysProcAttr)
}

// ApplyChildLifetime is the exported alias used by tools/shell.go. Same
// as applyChildLifetime — separate name so the tools package can import
// without reaching into unexported names.
func ApplyChildLifetime(cmd *exec.Cmd) { applyChildLifetime(cmd) }

// AttachAndResume is the post-Start hook (no-op on Unix, attaches to
// JobObject on Windows).
func AttachAndResume(cmd *exec.Cmd) error { return attachAndResume(cmd) }
