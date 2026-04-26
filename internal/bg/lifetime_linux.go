//go:build linux

package bg

import "syscall"

// On Linux, set PDEATHSIG so the kernel sends SIGTERM to the child if
// the parent (acorn) dies for any reason — including SIGKILL.
func setPdeathsig(s *syscall.SysProcAttr) {
	s.Pdeathsig = syscall.SIGTERM
}
