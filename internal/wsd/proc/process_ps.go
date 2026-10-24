//go:build darwin || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package proc

import (
	"os/exec"
	"strconv"
	"strings"
)

// ps is a local-driver fallback. Production Linux always uses /proc.
func listProcesses() ([]processInfo, error) {
	cmd := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,stat=")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// On macOS ps reports itself in its own output. It is a short-lived helper,
	// not an agent orphan, and must never become a reap target.
	psPID := cmd.Process.Pid
	out := make([]processInfo, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		if pid == psPID {
			continue
		}
		ppid, err2 := strconv.Atoi(fields[1])
		pgid, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		state := fields[3]
		if state != "" {
			state = state[:1]
		}
		out = append(out, processInfo{pid: pid, ppid: ppid, pgid: pgid, state: state})
	}
	return out, nil
}
