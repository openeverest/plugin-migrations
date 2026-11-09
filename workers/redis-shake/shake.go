package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	shakeStatusPort = 2244
	shakeStopGrace  = 15 * time.Second
)

// shakeLua runs in RedisShake for every command it is about to write. Cluster
// targets reject multi-key commands whose keys hash to different slots;
// commands with independent keys are split. Key exclusion is done here, not
// with RedisShake's block_key_prefix, which silently drops a whole command
// when only some of its keys are excluded. Commands that cannot be applied
// correctly stop the migration with a message.
const shakeLua = `
local EXCLUDE = {%s}
local function excluded(key)
  for _, prefix in ipairs(EXCLUDE) do
    if string.sub(key, 1, #prefix) == prefix then return true end
  end
  return false
end
if DB ~= 0 then
  error("the source changed database " .. DB .. ": only database 0 can be migrated into a Valkey cluster")
end
if CMD == "PUBLISH" or CMD == "SPUBLISH" then return end
local kept, crossSlot = 0, false
for i = 1, #KEYS do
  if not excluded(KEYS[i]) then kept = kept + 1 end
  if SLOTS[i] ~= SLOTS[1] then crossSlot = true end
end
if kept == #KEYS and not crossSlot then
  shake.call(DB, ARGV)
  return
end
if kept == 0 then return end
if CMD == "MSET" or CMD == "MSETNX" then
  for i = 2, #ARGV, 2 do
    if not excluded(ARGV[i]) then shake.call(DB, {"SET", ARGV[i], ARGV[i + 1]}) end
  end
  return
end
if CMD == "DEL" or CMD == "UNLINK" then
  for i = 2, #ARGV do
    if not excluded(ARGV[i]) then shake.call(DB, {CMD, ARGV[i]}) end
  end
  return
end
if crossSlot then
  error("cannot replicate " .. CMD .. ": its keys hash to different cluster slots")
end
error("cannot replicate " .. CMD .. ": it changes both excluded and migrated keys")
`

// luaString quotes s for Lua with decimal escapes, so the code contains no
// quotes or newlines from user input.
func luaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(":_-./", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "\\%03d", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlString quotes s as a TOML basic string; JSON string escaping is valid TOML.
func tomlString(s string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return strings.TrimSpace(buf.String())
}

// shakeConfig is the RedisShake configuration of one run. It contains
// credentials: write it with mode 0600 only.
func shakeConfig(source, target Endpoint, sourceCluster, targetCluster bool, options Options, dir string) string {
	prefixes := make([]string, len(options.ExcludeKeyPrefixes))
	for i, prefix := range options.ExcludeKeyPrefixes {
		prefixes[i] = luaString(prefix)
	}
	var b strings.Builder
	section := func(name string, endpoint Endpoint, cluster bool) {
		fmt.Fprintf(&b, "[%s]\ncluster = %t\naddress = %s\nusername = %s\npassword = %s\ntls = %t\n",
			name, cluster, tomlString(endpoint.Address()), tomlString(endpoint.User), tomlString(endpoint.Password), endpoint.TLS)
		// RedisShake 4.7 uses tls_config only when all three files are set,
		// and never verifies the server.
		if endpoint.Cert != "" && endpoint.CA != "" {
			fmt.Fprintf(&b, "tls_config = { ca_cert = %s, cert = %s, key = %s }\n",
				tomlString(endpoint.CA), tomlString(endpoint.Cert), tomlString(endpoint.Key))
		}
		b.WriteString("\n")
	}
	section("sync_reader", source, sourceCluster)
	b.WriteString("sync_rdb = true\nsync_aof = true\n\n")
	section("redis_writer", target, targetCluster)
	fmt.Fprintf(&b, "[filter]\nfunction = '''%s'''\n\n", fmt.Sprintf(shakeLua, strings.Join(prefixes, ", ")))
	fmt.Fprintf(&b, "[advanced]\ndir = %s\nstatus_port = %d\nlog_file = \"shake.log\"\nlog_level = \"info\"\nlog_interval = 30\n"+
		"rdb_restore_command_behavior = \"rewrite\"\nempty_db_before_sync = false\n", tomlString(dir), shakeStatusPort)
	return b.String()
}

// shakeProcess is one RedisShake run.
type shakeProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu        sync.Mutex
	lastError string
	exitErr   error
}

func startShake(binary, configPath string) (*shakeProcess, error) {
	cmd := exec.Command(binary, configPath)
	cmd.Dir = os.TempDir()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start redis-shake: %w", err)
	}
	p := &shakeProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.follow(output)
		err := cmd.Wait()
		p.mu.Lock()
		p.exitErr = err
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// follow copies RedisShake's output to the agent's log, without stack frames
// and with command arguments (user data) removed, and keeps the last error.
func (p *shakeProcess) follow(output io.Reader) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := ansiPattern.ReplaceAllString(scanner.Text(), "")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "RedisShake/") || strings.HasPrefix(trimmed, "runtime/") || strings.HasPrefix(trimmed, "redis-shake/") {
			continue
		}
		line = redactArguments(line)
		log.Printf("redis-shake: %s", line)
		if strings.Contains(line, " ERR ") {
			p.mu.Lock()
			p.lastError = shakeErrorMessage(line)
			p.mu.Unlock()
		}
	}
}

// exited reports whether the run ended and, if it did, how.
func (p *shakeProcess) exited() (bool, error, string) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return true, p.exitErr, p.lastError
	default:
		return false, nil, ""
	}
}

// stop asks RedisShake to finish writing and exit, then kills it.
func (p *shakeProcess) stop() {
	if exited, _, _ := p.exited(); exited {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(shakeStopGrace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// ansiPattern matches the color codes of RedisShake's console log.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

var argumentsPattern = regexp.MustCompile(`(cmd|argv)=\[([^\s\]]+)[^\n]*?\](,|$)`)

// redactArguments keeps only the command name of "cmd=[...]" and
// "argv=[...]": the arguments are keys and values.
func redactArguments(line string) string {
	return argumentsPattern.ReplaceAllString(line, "$1=[$2 …]$3")
}

var (
	logPrefixPattern = regexp.MustCompile(`^\S+ \S+ ERR (\[[^\]]*\] )?`)
	luaErrorPattern  = regexp.MustCompile(`load function script failed: <string>:\d+: (.*)`)
	errorPattern     = regexp.MustCompile(`error=\[(.*)\]\s*$`)
	commandPattern   = regexp.MustCompile(`cmd=\[([^\s\]]+)`)
)

// shakeErrorMessage turns a RedisShake ERR log line into a short reason.
func shakeErrorMessage(line string) string {
	line = strings.TrimSpace(redactArguments(line))
	if match := luaErrorPattern.FindStringSubmatch(line); match != nil {
		return strings.TrimSpace(match[1])
	}
	if match := errorPattern.FindStringSubmatch(line); match != nil {
		reason := strings.TrimSpace(match[1])
		if command := commandPattern.FindStringSubmatch(line); command != nil {
			reason = fmt.Sprintf("writing %s to the target failed: %s", strings.ToUpper(command[1]), reason)
		}
		return reason
	}
	return strings.TrimSpace(logPrefixPattern.ReplaceAllString(line, ""))
}

// transientPatterns mark failures worth a fresh copy: connections that broke
// or servers that were briefly unavailable. Anything else will fail again.
var transientPatterns = []string{
	"connection reset", "connection refused", "broken pipe", "EOF", "i/o timeout", "dial failed", "no route to host",
	"LOADING", "MASTERDOWN", "CLUSTERDOWN", "TRYAGAIN", "MOVED", "ASK ", "READONLY", "NOREPLICAS", "BUSY ",
	"use of closed network connection", "server closed", "dial tcp", "no such host", "failed to dial",
	"deadline exceeded", "timeout", "network is unreachable", "CLUSTERDOWN",
}

func transientError(message string) bool {
	for _, pattern := range transientPatterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

// shakeStatus is RedisShake's status_port response.
type shakeStatus struct {
	Readers []shakeReader
}

type shakeReader struct {
	Status           string `json:"status"`
	RdbFileSizeBytes uint64 `json:"rdb_file_size_bytes"`
	RdbReceivedBytes uint64 `json:"rdb_received_bytes"`
	RdbSentBytes     uint64 `json:"rdb_sent_bytes"`
}

// readerAof is the state of a reader that applies changes after the copy.
const readerAof = "syncing aof"

func (s shakeStatus) copyDone() bool {
	if len(s.Readers) == 0 {
		return false
	}
	for _, reader := range s.Readers {
		if reader.Status != readerAof {
			return false
		}
	}
	return true
}

// copyBytes is the snapshot size and how much of it was applied, summed over
// the source shards.
func (s shakeStatus) copyBytes() (total, copied uint64) {
	for _, reader := range s.Readers {
		total += reader.RdbFileSizeBytes
		copied += min(reader.RdbSentBytes, reader.RdbFileSizeBytes)
	}
	return total, copied
}

// parseShakeStatus reads the status body; the reader is an object for a
// standalone source and a list for a cluster.
func parseShakeStatus(body []byte) (shakeStatus, error) {
	var raw struct {
		Reader json.RawMessage `json:"reader"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return shakeStatus{}, err
	}
	var status shakeStatus
	if err := decodeOneOrMany(raw.Reader, &status.Readers); err != nil {
		return shakeStatus{}, fmt.Errorf("reader: %w", err)
	}
	return status, nil
}

func decodeOneOrMany[T any](raw json.RawMessage, into *[]T) error {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return nil
	case raw[0] == '[':
		return json.Unmarshal(raw, into)
	}
	var one T
	if err := json.Unmarshal(raw, &one); err != nil {
		return err
	}
	*into = []T{one}
	return nil
}

var statusClient = &http.Client{Timeout: 5 * time.Second}

func fetchShakeStatus(ctx context.Context) (shakeStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(shakeStatusPort)+"/", nil)
	if err != nil {
		return shakeStatus{}, err
	}
	response, err := statusClient.Do(request)
	if err != nil {
		return shakeStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return shakeStatus{}, errors.New(response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return shakeStatus{}, err
	}
	return parseShakeStatus(body)
}
