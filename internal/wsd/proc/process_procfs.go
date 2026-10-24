//go:build linux

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func listProcesses() ([]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]processInfo, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		text := string(raw)
		end := strings.LastIndex(text, ")")
		if end < 0 || end+2 >= len(text) {
			continue
		}
		p, err := parseStatFields(pid, text[end+2:])
		if err == nil {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no readable processes under /proc")
	}
	return out, nil
}
