//go:build !windows

package bg

import "os/exec"

// attachAndResume is a no-op on Unix — Pdeathsig is set on the
// SysProcAttr before Start(), and the kernel does the rest.
func attachAndResume(_ *exec.Cmd) error { return nil }
