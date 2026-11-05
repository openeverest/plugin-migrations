package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// psql runs SQL from stdin. Values are passed as psql variables and quoted by
// psql (:'name' for literals, :"name" for identifiers), never concatenated.
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
		return "", fmt.Errorf("%s", errorText(stderr.String(), err))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// psqlJSON runs a query returning a single JSON value and decodes it.
func psqlJSON(ctx context.Context, uri, sql string, vars map[string]string, destination any) error {
	output, err := psql(ctx, uri, sql, vars)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(output), destination); err != nil {
		return fmt.Errorf("unexpected query result: %w", err)
	}
	return nil
}

// runTool runs pg_dump or pg_restore and reports their error output.
func runTool(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %s", name, errorText(stderr.String(), err))
	}
	return nil
}

// errorText keeps the first error lines PostgreSQL tools print. They name
// hosts and objects but never passwords.
func errorText(stderr string, fallback error) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "psql:<stdin>:"), "pg_restore: "))
		if len(lines) == 3 {
			break
		}
	}
	if len(lines) == 0 {
		return fallback.Error()
	}
	return strings.Join(lines, " ")
}

// psqlCtx and psqlJSONCtx bound each statement by sqlTimeout within ctx.
func psqlCtx(ctx context.Context, uri, sql string, vars map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	return psql(ctx, uri, sql, vars)
}

func psqlJSONCtx(ctx context.Context, uri, sql string, vars map[string]string, destination any) error {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	return psqlJSON(ctx, uri, sql, vars, destination)
}

func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return "[]"
	}
	return string(encoded)
}
