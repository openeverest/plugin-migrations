package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// MariaDB copies stream mydumper's output into myloader, so the worker needs
// no disk for the data. The stream is a sequence of files, each framed as
// "\n-- <name> <size>\n" followed by <size> bytes. The agent forwards it and
// reads the source position from the "metadata" file mydumper sends last.

const (
	metadataFile   = "metadata"
	maxStderrBytes = 64 << 10
)

var mariadbPositionPattern = regexp.MustCompile(`^[0-9]+-[0-9]+-[0-9]+(,[0-9]+-[0-9]+-[0-9]+)*$`)

// dataFilePattern matches table data chunks (db.table.00000.sql), not schema files.
var dataFilePattern = regexp.MustCompile(`^[^/]+\.[^/]+\.[0-9]+\.sql$`)

func runMydumperCopy(ctx context.Context, workDir string, source, target Endpoint, options Options, progress func(uint64)) (string, error) {
	dumpConfig, err := writeOptionFile(workDir, "dump.cnf", "mydumper", source)
	if err != nil {
		return "", err
	}
	loadConfig, err := writeOptionFile(workDir, "load.cnf", "myloader", target)
	if err != nil {
		return "", err
	}
	omit := workDir + "/omit.txt"
	if err := os.WriteFile(omit, []byte(strings.Join(options.ExcludeTables, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write the excluded tables: %w", err)
	}
	// Both tools refuse an existing directory in stream mode.
	dumpDir, loadDir := workDir+"/dump", workDir+"/load"
	for _, dir := range []string{dumpDir, loadDir} {
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("clean the work directory: %w", err)
		}
	}

	parent := ctx
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	threads := strconv.Itoa(options.CopyThreads)
	dumper := exec.CommandContext(ctx, "mydumper", "--defaults-file="+dumpConfig, "--database="+strings.Join(options.Databases, ","),
		"--routines", "--events", "--triggers", "--omit-from-file="+omit, "--threads="+threads, "--stream", "--outputdir="+dumpDir, "--verbose=2")
	loader := exec.CommandContext(ctx, "myloader", "--defaults-file="+loadConfig, "--stream", "--threads="+threads,
		"--directory="+loadDir, "--verbose=2")
	dumperErr, loaderErr := &limitedBuffer{max: maxStderrBytes}, &limitedBuffer{max: maxStderrBytes}
	dumper.Stderr, loader.Stderr, loader.Stdout = dumperErr, loaderErr, loaderErr
	stream, err := dumper.StdoutPipe()
	if err != nil {
		return "", err
	}
	loaderIn, err := loader.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := loader.Start(); err != nil {
		return "", fmt.Errorf("start myloader: %w", err)
	}
	if err := dumper.Start(); err != nil {
		cancel()
		_ = loader.Wait()
		return "", fmt.Errorf("start mydumper: %w", err)
	}

	toLoader := &recordingWriter{w: loaderIn}
	position, forwardErr := forwardStream(stream, toLoader, progress)
	_ = loaderIn.Close()
	if forwardErr != nil {
		// The tool that did not fail is killed; the errors below name the
		// one that failed by itself.
		cancel()
	}
	dumpErr, loadErr := dumper.Wait(), loader.Wait()
	switch {
	case parent.Err() != nil:
		return "", parent.Err()
	case toLoader.err != nil && loadErr != nil:
		return "", fmt.Errorf("copy failed: myloader: %s", toolFailure(loaderErr.String(), loadErr))
	case exitedWithError(dumper, dumpErr):
		return "", fmt.Errorf("copy failed: mydumper: %s", toolFailure(dumperErr.String(), dumpErr))
	case exitedWithError(loader, loadErr):
		return "", fmt.Errorf("copy failed: myloader: %s", toolFailure(loaderErr.String(), loadErr))
	case forwardErr != nil:
		return "", fmt.Errorf("copy failed: %w", forwardErr)
	case dumpErr != nil || loadErr != nil:
		return "", fmt.Errorf("copy failed: mydumper: %v, myloader: %v", dumpErr, loadErr)
	case position == "":
		return "", errors.New("mydumper did not report the source position; is binary logging enabled on the source?")
	case !mariadbPositionPattern.MatchString(position):
		return "", errors.New("mydumper reported a source position the agent cannot read")
	}
	return position, nil
}

// forwardStream copies the stream to w and returns the executed_gtid_set of
// the metadata file. progress receives the size of the data files so far.
func forwardStream(stream io.Reader, w io.Writer, progress func(uint64)) (string, error) {
	reader := bufio.NewReaderSize(stream, 1<<20)
	var position string
	var loaded uint64
	for {
		header, err := reader.ReadString('\n')
		if err == io.EOF && header == "" {
			return position, nil
		}
		if err != nil {
			return "", fmt.Errorf("read the dump stream: %w", err)
		}
		if header == "\n" {
			// Each file header is preceded by an empty line.
			header, err = reader.ReadString('\n')
			if err != nil {
				return "", fmt.Errorf("read the dump stream: %w", err)
			}
		}
		name, size, err := parseFrameHeader(header)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(w, "\n"+header); err != nil {
			return "", fmt.Errorf("write to myloader: %w", err)
		}
		body := io.LimitReader(reader, int64(size))
		if name == metadataFile {
			var content bytes.Buffer
			if _, err := io.CopyN(io.MultiWriter(w, &content), body, int64(size)); err != nil {
				return "", fmt.Errorf("forward %s: %w", name, err)
			}
			position = parseExecutedGTIDSetFromMetadata(content.String())
		} else if _, err := io.CopyN(w, body, int64(size)); err != nil {
			return "", fmt.Errorf("forward %s: %w", name, err)
		}
		if dataFilePattern.MatchString(name) && progress != nil {
			loaded += size
			progress(loaded)
		}
	}
}

func parseFrameHeader(header string) (string, uint64, error) {
	line := strings.TrimSuffix(header, "\n")
	rest, found := strings.CutPrefix(line, "-- ")
	at := strings.LastIndex(rest, " ")
	if !found || at <= 0 {
		return "", 0, fmt.Errorf("unexpected dump stream header %q", truncate(line, 80))
	}
	size, err := strconv.ParseUint(rest[at+1:], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("unexpected dump stream header %q", truncate(line, 80))
	}
	return rest[:at], size, nil
}

var executedGTIDSetLine = regexp.MustCompile(`(?m)^#\s*executed_gtid_set\s*=\s*"([^"]*)"`)

func parseExecutedGTIDSetFromMetadata(metadata string) string {
	match := executedGTIDSetLine.FindStringSubmatch(metadata)
	if match == nil {
		return ""
	}
	return strings.Join(strings.Fields(match[1]), "")
}

// writeOptionFile writes credentials for one tool, so they never appear in
// the process list. mydumper and myloader hand host, user and password to the
// MySQL client library, which reads them with MySQL's option-file syntax
// (unquoted "#" starts a comment), so those are quoted. The other keys are
// read raw through GLib and passed on as options.
func writeOptionFile(workDir, name, section string, endpoint Endpoint) (string, error) {
	if strings.ContainsAny(endpoint.SSLCA, "\n\r") {
		return "", fmt.Errorf("write %s: invalid CA path", name)
	}
	lines := []string{
		"[" + section + "]",
		"host=" + optionFileValue(endpoint.Host),
		"port=" + strconv.Itoa(endpoint.Port),
		"user=" + optionFileValue(endpoint.User),
		"password=" + optionFileValue(endpoint.Password),
		"ssl-mode=" + endpoint.SSLMode,
	}
	if endpoint.SSLCA != "" {
		lines = append(lines, "ca="+endpoint.SSLCA)
	}
	path := workDir + "/" + name
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return path, nil
}

// optionFileValue double-quotes a value for MySQL's option-file parser, which
// strips the quotes and decodes these escapes.
func optionFileValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(value) + `"`
}

// toolFailure keeps mydumper/myloader error lines; they name servers and
// objects, never passwords.
func toolFailure(stderr string, fallback error) string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "CRITICAL") || strings.Contains(line, "ERROR") || strings.Contains(line, "Error") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	if len(lines) == 0 {
		return fallback.Error()
	}
	return strings.Join(lines, " ")
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

// exitedWithError tells a tool that failed by itself from one the agent killed.
func exitedWithError(cmd *exec.Cmd, err error) bool {
	return err != nil && cmd.ProcessState != nil && cmd.ProcessState.Exited()
}

// recordingWriter remembers the first write error, which tells that myloader
// stopped reading.
type recordingWriter struct {
	w   io.Writer
	err error
}

func (r *recordingWriter) Write(data []byte) (int, error) {
	n, err := r.w.Write(data)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n, err
}

// limitedBuffer keeps the last max bytes written to it.
type limitedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, data...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
