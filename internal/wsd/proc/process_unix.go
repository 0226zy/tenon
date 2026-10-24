//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package proc

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalNotifySIGCHLD(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGCHLD) }
func signalStop(ch chan<- os.Signal)          { signal.Stop(ch) }
