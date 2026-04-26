//go:build !windows && !linux

package bg

import "syscall"

// macOS / BSD: no kernel-level PDEATHSIG equivalent. Children may
// outlive the parent on a hard crash. KillAll on graceful shutdown
// covers the normal exit case; the rest is best-effort.
func setPdeathsig(s *syscall.SysProcAttr) {}
