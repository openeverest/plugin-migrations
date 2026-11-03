package main

import (
	"bytes"
	"strings"
	"sync"
)

// lineRing keeps the last lines pgcopydb logged, so a failure can be explained
// without reading the pod log. pgcopydb logs connection strings without
// passwords.
type lineRing struct {
	mu      sync.Mutex
	size    int
	lines   []string
	partial []byte
}

func newLineRing(size int) *lineRing { return &lineRing{size: size} }

func (r *lineRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		index := bytes.IndexByte(r.partial, '\n')
		if index < 0 {
			break
		}
		r.lines = append(r.lines, string(r.partial[:index]))
		r.partial = r.partial[index+1:]
		if len(r.lines) > r.size {
			r.lines = r.lines[len(r.lines)-r.size:]
		}
	}
	return len(p), nil
}

// LastError returns the message of the most recent ERROR or FATAL line,
// without pgcopydb's timestamp, pid, level and source location columns.
func (r *lineRing) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.lines) - 1; i >= 0; i-- {
		line := r.lines[i]
		for _, level := range []string{" FATAL ", " ERROR "} {
			if index := strings.Index(line, level); index >= 0 {
				return messageAfterLocation(strings.TrimSpace(line[index+len(level):]))
			}
		}
	}
	return "See the worker log for details."
}

// messageAfterLocation strips a leading "file.c:123" column.
func messageAfterLocation(text string) string {
	location, message, ok := strings.Cut(text, " ")
	if ok && strings.Contains(location, ".c:") {
		return strings.TrimSpace(message)
	}
	return text
}
