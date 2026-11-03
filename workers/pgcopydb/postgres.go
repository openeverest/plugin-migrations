package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// parseLSN converts a PostgreSQL "X/Y" log sequence number to a byte position.
func parseLSN(lsn string) (uint64, error) {
	high, low, ok := strings.Cut(strings.TrimSpace(lsn), "/")
	if !ok {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	h, err := strconv.ParseUint(high, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	l, err := strconv.ParseUint(low, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	return h<<32 | l, nil
}

// psql runs SQL from stdin with psql variables, so values are interpolated
// with psql's quoting (:'name' and :"name") instead of string concatenation.
// It returns the unaligned, tuples-only output.
func psql(ctx context.Context, uri, sql string, vars map[string]string) (string, error) {
	args := []string{"--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "-v", "ON_ERROR_STOP=1", "--dbname", uri}
	for name, value := range vars {
		args = append(args, "-v", name+"="+value)
	}
	command := exec.CommandContext(ctx, "psql", args...)
	command.Stdin = strings.NewReader(sql)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("psql: %s", firstLine(stderr.String(), err.Error()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func firstLine(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	line, _, _ := strings.Cut(text, "\n")
	return line
}
