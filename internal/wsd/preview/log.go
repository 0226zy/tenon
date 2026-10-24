package preview

import (
	"bytes"
	"sync"
)

const maxLogBytes = 1 << 20

type logRing struct {
	mu       sync.Mutex
	data     []byte
	maxLines int
}

func newLogRing(maxLines int) *logRing {
	if maxLines <= 0 || maxLines > 2000 {
		maxLines = 2000
	}
	return &logRing{maxLines: maxLines}
}

func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.data = append(l.data, p...)
	l.trimLocked()
	l.mu.Unlock()
	return len(p), nil
}

func (l *logRing) trimLocked() {
	if len(l.data) > maxLogBytes {
		excess := len(l.data) - maxLogBytes
		cut := excess
		if newline := bytes.IndexByte(l.data[excess:], '\n'); newline >= 0 {
			cut = excess + newline + 1
		}
		if cut > len(l.data) {
			cut = len(l.data)
		}
		l.data = append(l.data[:0], l.data[cut:]...)
	}
	for bytes.Count(l.data, []byte{'\n'}) > l.maxLines {
		cut := bytes.IndexByte(l.data, '\n') + 1
		l.data = append(l.data[:0], l.data[cut:]...)
	}
}

func (l *logRing) Tail(lines int) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lines <= 0 || lines > 2000 {
		lines = 200
	}
	data := l.data
	if len(data) > maxLogBytes {
		data = data[len(data)-maxLogBytes:]
	}
	start := 0
	seen := 0
	scanEnd := len(data)
	if scanEnd > 0 && data[scanEnd-1] == '\n' {
		scanEnd--
	}
	for i := scanEnd - 1; i >= 0; i-- {
		if data[i] == '\n' {
			seen++
			if seen == lines {
				start = i + 1
				break
			}
		}
	}
	return append([]byte(nil), data[start:]...)
}
