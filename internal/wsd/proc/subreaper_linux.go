//go:build linux

package proc

import (
	"os"
	"syscall"
)

const prSetChildSubreaper = 36

// SetSubreaper lets orphaned agent descendants be adopted by wsd (PID 1).
func SetSubreaper() error {
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
	if errno != 0 {
		return os.NewSyscallError("prctl(PR_SET_CHILD_SUBREAPER)", errno)
	}
	return nil
}
