//go:build windows

package bg

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows we put every child into a Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. When acorn exits (graceful or
// otherwise — including SIGKILL-equivalent), the Job Object's last
// handle closes and Windows terminates every assigned process.
//
// One job is shared by all children; created lazily on first use.
var sharedJob windows.Handle

func ensureJob() (windows.Handle, error) {
	if sharedJob != 0 {
		return sharedJob, nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	type basicLimit struct {
		PerProcessUserTimeLimit int64
		PerJobUserTimeLimit     int64
		LimitFlags              uint32
		MinimumWorkingSetSize   uintptr
		MaximumWorkingSetSize   uintptr
		ActiveProcessLimit      uint32
		Affinity                uintptr
		PriorityClass           uint32
		SchedulingClass         uint32
	}
	type extLimit struct {
		Basic              basicLimit
		IoInfo             [48]byte
		ProcessMemoryLimit uintptr
		JobMemoryLimit     uintptr
		PeakProcessMem     uintptr
		PeakJobMem         uintptr
	}
	const (
		jobObjectExtendedLimitInformation = 9
		jobLimitKillOnJobClose            = 0x00002000
	)
	var info extLimit
	info.Basic.LimitFlags = jobLimitKillOnJobClose
	_, err = windows.SetInformationJobObject(
		h,
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	sharedJob = h
	return h, nil
}

// applyChildLifetime is a no-op on Windows — the actual job assignment
// happens AFTER cmd.Start() in attachAndResume because we need the
// process handle. We set up the job here lazily so launch can fail
// fast if the OS won't let us create one.
func applyChildLifetime(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	_, _ = ensureJob()
}

// ApplyChildLifetime — exported alias for tools/shell.go.
func ApplyChildLifetime(cmd *exec.Cmd) { applyChildLifetime(cmd) }

// AttachAndResume — exported alias.
func AttachAndResume(cmd *exec.Cmd) error { return attachAndResume(cmd) }

// attachAndResume on Windows assigns the started child to the shared
// Job Object so it gets killed when acorn dies. Brief race: the child
// may execute a few instructions before being attached. Acceptable
// trade-off vs the suspend-then-resume dance.
func attachAndResume(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	job, err := ensureJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h)
}
