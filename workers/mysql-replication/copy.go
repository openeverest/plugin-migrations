package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// copyScript runs in MySQL Shell. The configuration, credentials included,
// arrives in the environment so it never shows up in the process list.
const copyScript = `
var config = JSON.parse(os.getenv('MIGRATION_COPY'));
shell.connect(config.source);
util.copySchemas(config.databases, config.target, config.options);
`

type copyConfig struct {
	Source    map[string]any `json:"source"`
	Target    map[string]any `json:"target"`
	Databases []string       `json:"databases"`
	Options   map[string]any `json:"options"`
}

// copyOptions are util.copySchemas options. The GTID set is appended by the
// agent (see appendGTIDPurgedSQL), so a retried migration does not fail on
// GTIDs the target already has. skipBinlog stays off: on PXC the binary log
// is what Galera replicates to the other nodes.
func copyOptions(options Options, exclusions map[string][]string) map[string]any {
	result := map[string]any{
		"threads":       options.CopyThreads,
		"consistent":    true,
		"showProgress":  true,
		"updateGtidSet": "off",
		"skipBinlog":    false,
	}
	excluded := map[string][]string{"excludeTables": options.ExcludeTables}
	for option, names := range exclusions {
		excluded[option] = append(excluded[option], names...)
	}
	for option, names := range excluded {
		if len(names) > 0 {
			result[option] = names
		}
	}
	return result
}

// runCopy copies the databases with MySQL Shell and returns the source GTID
// set the copy is consistent with. progress receives the bytes loaded so far.
func runCopy(ctx context.Context, workDir string, source, target Endpoint, options Options, exclusions map[string][]string,
	progress func(loaded uint64)) (string, error) {
	config, err := json.Marshal(copyConfig{
		Source: source.ShellConnection(), Target: target.ShellConnection(),
		Databases: options.Databases, Options: copyOptions(options, exclusions),
	})
	if err != nil {
		return "", fmt.Errorf("encode the copy configuration: %w", err)
	}
	script := workDir + "/copy.js"
	if err := os.WriteFile(script, []byte(copyScript), 0o600); err != nil {
		return "", fmt.Errorf("write the copy script: %w", err)
	}
	command := exec.CommandContext(ctx, "mysqlsh", "--no-defaults", "--js", "--quiet-start=2", "--file", script)
	command.Env = append(os.Environ(), "MIGRATION_COPY="+string(config), "HOME="+workDir, "MYSQLSH_USER_CONFIG_HOME="+workDir)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start MySQL Shell: %w", err)
	}
	output := newCopyOutput(progress)
	_, _ = io.Copy(output, stdout)
	waitErr := command.Wait()
	output.flush()
	if waitErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("copy failed: %s", output.failure(waitErr))
	}
	gtids, err := parseExecutedGTIDSet(output.text())
	if err != nil {
		return "", err
	}
	return gtids, nil
}

// copyOutput collects MySQL Shell output. Progress lines are rewritten in
// place with carriage returns, so both \r and \n end a line.
type copyOutput struct {
	progress func(uint64)
	partial  []byte
	lines    []string
}

func newCopyOutput(progress func(uint64)) *copyOutput { return &copyOutput{progress: progress} }

const maxKeptLines = 400

func (o *copyOutput) Write(data []byte) (int, error) {
	o.partial = append(o.partial, data...)
	for {
		index := bytes.IndexAny(o.partial, "\r\n")
		if index < 0 {
			break
		}
		o.line(string(o.partial[:index]))
		o.partial = o.partial[index+1:]
	}
	return len(data), nil
}

func (o *copyOutput) flush() {
	if len(o.partial) > 0 {
		o.line(string(o.partial))
		o.partial = nil
	}
}

func (o *copyOutput) line(line string) {
	if loaded, ok := parseLoadedBytes(line); ok {
		if o.progress != nil {
			o.progress(loaded)
		}
		return
	}
	if strings.TrimSpace(line) == "" || strings.Contains(line, "thds dumping") {
		return
	}
	o.lines = append(o.lines, line)
	if len(o.lines) > maxKeptLines {
		o.lines = o.lines[len(o.lines)-maxKeptLines:]
	}
}

func (o *copyOutput) text() string { return strings.Join(o.lines, "\n") }

// failure returns MySQL Shell's error lines. They name servers and objects,
// never passwords.
func (o *copyOutput) failure(fallback error) string {
	var errorsFound []string
	for _, line := range o.lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ERROR:") || strings.Contains(trimmed, "(MYSQLSH ") || strings.Contains(trimmed, "MySQL Error") {
			errorsFound = append(errorsFound, strings.TrimSpace(strings.TrimPrefix(trimmed, "ERROR:")))
		}
	}
	if len(errorsFound) > 3 {
		errorsFound = errorsFound[len(errorsFound)-3:]
	}
	if len(errorsFound) == 0 {
		return fallback.Error()
	}
	return strings.Join(errorsFound, " ")
}

// loadProgressPattern matches the loader's progress, e.g.
// "100% (83.09 MB / 83.09 MB), 4.96 MB/s (25.14K rows/s), 1 / 1 tables done"
// or, while the dump is still running, "?% (12.00 MB / ?), ...".
var loadProgressPattern = regexp.MustCompile(`\(([0-9.]+) ([KMGTP]?B) / [^)]*\).*tables done`)

func parseLoadedBytes(line string) (uint64, bool) {
	match := loadProgressPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	multiplier := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}[match[2]]
	return uint64(value * multiplier), true
}

var gtidSetPattern = regexp.MustCompile(`^([0-9a-fA-F-]{36}(:[A-Za-z0-9_]{1,32})?(:[0-9]+(-[0-9]+)?)+)(,[0-9a-fA-F-]{36}(:[A-Za-z0-9_]{1,32})?(:[0-9]+(-[0-9]+)?)+)*$`)

// parseExecutedGTIDSet reads Executed_GTID_set from the Dump_metadata block
// MySQL Shell prints at the end of a copy. Long sets are printed as a YAML
// block ("|-" followed by indented lines).
func parseExecutedGTIDSet(output string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	var value strings.Builder
	collecting := false
	for scanner.Scan() {
		line := scanner.Text()
		if collecting {
			if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, "   ") {
				break
			}
			value.WriteString(strings.TrimSpace(line))
			continue
		}
		_, rest, found := strings.Cut(line, "Executed_GTID_set:")
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "|-" || rest == "|" || rest == ">-" {
			collecting = true
			continue
		}
		value.WriteString(strings.Trim(rest, `'"`))
		break
	}
	set := strings.Join(strings.Fields(value.String()), "")
	if set == "" {
		return "", errors.New("MySQL Shell did not report the GTID set of the copy; is GTID mode enabled on the source?")
	}
	if !gtidSetPattern.MatchString(set) {
		return "", errors.New("MySQL Shell reported a GTID set the agent cannot read")
	}
	return set, nil
}
