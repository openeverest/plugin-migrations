package main

import (
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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// The DM cluster lives inside the worker pod: one dm-master (with its
// embedded etcd) and one dm-worker, both listening on the loopback only.
// Their state is on the worker's volume, so a restarted pod continues the
// task where it stopped.
const (
	dmMasterAddr = "127.0.0.1:8261"
	dmPeerAddr   = "127.0.0.1:8291"
	dmWorkerAddr = "127.0.0.1:8262"
	dmWorkerName = "worker"
	dmMasterName = "master"
	dmTimeout    = 30 * time.Second
	// dmStartTimeout bounds creating a task, which DM may check first.
	dmStartTimeout = 5 * time.Minute
	// checkpointFlushSeconds bounds how far DM's saved position lags behind
	// what it applied; the cutover waits for the saved position.
	checkpointFlushSeconds = 5
	maxDMResponseBytes     = 4 << 20
)

// DM drives the local DM cluster.
type DM struct {
	binDir    string
	dataDir   string
	http      *http.Client
	startHTTP *http.Client
	base      string
}

func NewDM(binDir, dataDir string) *DM {
	return &DM{binDir: binDir, dataDir: dataDir, http: &http.Client{Timeout: dmTimeout}, startHTTP: &http.Client{Timeout: dmStartTimeout},
		base: "http://" + dmMasterAddr}
}

func (d *DM) masterDir() string { return filepath.Join(d.dataDir, "dm-master") }
func (d *DM) workerDir() string { return filepath.Join(d.dataDir, "dm-worker") }

// DumpDir is where DM's dump of the databases is staged until it is loaded.
func (d *DM) DumpDir(name string) string { return filepath.Join(d.dataDir, "dump", name) }

// Run starts dm-master and dm-worker and restarts them when they exit, until
// ctx ends; then it stops both. Their logs go to the worker's output.
func (d *DM) Run(ctx context.Context) error {
	for _, dir := range []string{d.masterDir(), d.workerDir(), filepath.Join(d.dataDir, "dump")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	masterConfig := strings.Join([]string{
		`name = "` + dmMasterName + `"`,
		`master-addr = "` + dmMasterAddr + `"`,
		`advertise-addr = "` + dmMasterAddr + `"`,
		`peer-urls = "` + dmPeerAddr + `"`,
		`advertise-peer-urls = "http://` + dmPeerAddr + `"`,
		`initial-cluster = "` + dmMasterName + `=http://` + dmPeerAddr + `"`,
		`data-dir = "` + filepath.Join(d.masterDir(), "data") + `"`,
		`openapi = true`,
	}, "\n") + "\n"
	workerConfig := strings.Join([]string{
		`name = "` + dmWorkerName + `"`,
		`worker-addr = "` + dmWorkerAddr + `"`,
		`advertise-addr = "` + dmWorkerAddr + `"`,
		`join = "` + dmMasterAddr + `"`,
		`relay-dir = "` + filepath.Join(d.workerDir(), "relay") + `"`,
	}, "\n") + "\n"
	masterPath, workerPath := filepath.Join(d.masterDir(), "dm-master.toml"), filepath.Join(d.workerDir(), "dm-worker.toml")
	if err := os.WriteFile(masterPath, []byte(masterConfig), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(workerPath, []byte(workerConfig), 0o600); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() {
		superviseProcess(ctx, "dm-master", filepath.Join(d.binDir, "dm-master"), d.masterDir(), "--config="+masterPath)
		done <- struct{}{}
	}()
	go func() {
		superviseProcess(ctx, "dm-worker", filepath.Join(d.binDir, "dm-worker"), d.workerDir(), "--config="+workerPath)
		done <- struct{}{}
	}()
	<-done
	<-done
	return nil
}

// superviseProcess runs a DM process and starts it again whenever it exits.
func superviseProcess(ctx context.Context, name, path, dir string, args ...string) {
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command(path, args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			log.Printf("%s: start: %v", name, err)
		} else {
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err := <-exited:
				log.Printf("%s exited: %v", name, err)
			case <-ctx.Done():
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-exited:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-exited
				}
				return
			}
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, 30*time.Second)
	}
}

// Ready reports whether the master answers and the worker has registered.
func (d *DM) Ready(ctx context.Context) error {
	var response struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := d.call(ctx, http.MethodGet, "/api/v1/cluster/workers", nil, &response); err != nil {
		return err
	}
	for _, worker := range response.Data {
		if worker.Name == dmWorkerName {
			return nil
		}
	}
	return errors.New("the DM worker has not registered yet")
}

// gatewayResult is the common part of dm-master's HTTP gateway responses.
type gatewayResult struct {
	Result  bool   `json:"result"`
	Msg     string `json:"msg"`
	Sources []struct {
		Result bool   `json:"result"`
		Msg    string `json:"msg"`
	} `json:"sources"`
	CheckResult string `json:"checkResult"`
}

// failure returns the first error message of a gateway response.
func (r gatewayResult) failure() string {
	if !r.Result && r.Msg != "" {
		return r.Msg
	}
	for _, source := range r.Sources {
		if !source.Result && source.Msg != "" {
			return source.Msg
		}
	}
	if !r.Result {
		return "DM rejected the request"
	}
	return ""
}

// SourceExists tells whether DM knows the source.
func (d *DM) SourceExists(ctx context.Context, name string) (bool, error) {
	var response struct {
		Total int `json:"total"`
		Data  []struct {
			SourceName string `json:"source_name"`
		} `json:"data"`
	}
	if err := d.call(ctx, http.MethodGet, "/api/v1/sources", nil, &response); err != nil {
		return false, err
	}
	for _, source := range response.Data {
		if source.SourceName == name {
			return true, nil
		}
	}
	return false, nil
}

// CreateSource registers the source and binds it to the worker.
func (d *DM) CreateSource(ctx context.Context, config []byte) error {
	var response gatewayResult
	body := map[string]any{"op": "StartSource", "config": []string{string(config)}, "workerName": dmWorkerName}
	if err := d.call(ctx, http.MethodPut, "/apis/v1alpha1/sources", body, &response); err != nil {
		return err
	}
	if failure := response.failure(); failure != "" {
		return errors.New(cleanDMMessage(failure))
	}
	return nil
}

// StopSource removes the source; it disconnects from the source server.
func (d *DM) StopSource(ctx context.Context, name string) error {
	exists, err := d.SourceExists(ctx, name)
	if err != nil || !exists {
		return err
	}
	var response gatewayResult
	body := map[string]any{"op": "StopSource", "sourceID": []string{name}}
	if err := d.call(ctx, http.MethodPut, "/apis/v1alpha1/sources", body, &response); err != nil {
		return err
	}
	if failure := response.failure(); failure != "" {
		return errors.New(cleanDMMessage(failure))
	}
	return nil
}

// StartTask creates and starts the task. DM runs its precheck first and
// returns its result when it fails.
func (d *DM) StartTask(ctx context.Context, config []byte) error {
	var response gatewayResult
	if err := d.callWith(ctx, d.startHTTP, http.MethodPost, "/apis/v1alpha1/tasks", map[string]any{"task": string(config)}, &response); err != nil {
		return err
	}
	if response.Result {
		return nil
	}
	if response.CheckResult != "" {
		if check := parseCheckResult(response.CheckResult); len(check.Problems) > 0 {
			return errors.New(strings.Join(check.Problems, "; "))
		}
	}
	return errors.New(cleanDMMessage(response.failure()))
}

// Task operations of dm-master's gateway; Delete is dmctl's stop-task: it
// removes the task and keeps its checkpoints on the target.
const (
	taskStop   = "Delete"
	taskPause  = "Pause"
	taskResume = "Resume"
)

// OperateTask pauses, resumes or stops (removes) the task.
func (d *DM) OperateTask(ctx context.Context, name, op string) error {
	var response gatewayResult
	if err := d.call(ctx, http.MethodPut, "/apis/v1alpha1/tasks/"+name, map[string]any{"op": op}, &response); err != nil {
		return err
	}
	if failure := response.failure(); failure != "" {
		return errors.New(cleanDMMessage(failure))
	}
	return nil
}

// SkipEvent skips the failed binlog event at position ("file:pos") and
// resumes the task.
func (d *DM) SkipEvent(ctx context.Context, name, position string) error {
	var response gatewayResult
	body := map[string]any{"op": "Skip", "task": name, "binlogPos": position}
	if err := d.call(ctx, http.MethodPut, "/apis/v1alpha1/errors", body, &response); err != nil {
		return err
	}
	if failure := response.failure(); failure != "" {
		return errors.New(cleanDMMessage(failure))
	}
	return nil
}

// TaskStatus is the subtask status of a single-source task.
type TaskStatus struct {
	Stage  string `json:"stage"`
	Unit   string `json:"unit"`
	Result *struct {
		IsCanceled bool      `json:"isCanceled"`
		Errors     []DMError `json:"errors"`
	} `json:"result"`
	Dump *struct {
		TotalTables       flexNumber `json:"totalTables"`
		CompletedTables   flexNumber `json:"completedTables"`
		FinishedBytes     flexNumber `json:"finishedBytes"`
		FinishedRows      flexNumber `json:"finishedRows"`
		EstimateTotalRows flexNumber `json:"estimateTotalRows"`
	} `json:"dump"`
	Load *struct {
		FinishedBytes flexNumber `json:"finishedBytes"`
		TotalBytes    flexNumber `json:"totalBytes"`
	} `json:"load"`
	Sync *struct {
		MasterBinlog     string `json:"masterBinlog"`
		MasterBinlogGTID string `json:"masterBinlogGtid"`
		SyncerBinlog     string `json:"syncerBinlog"`
		SyncerBinlogGTID string `json:"syncerBinlogGtid"`
		Synced           bool   `json:"synced"`
	} `json:"sync"`
	Msg string `json:"msg"`
}

// DMError is one error of a paused subtask.
type DMError struct {
	ErrCode    int    `json:"ErrCode"`
	Message    string `json:"Message"`
	RawCause   string `json:"RawCause"`
	Workaround string `json:"Workaround"`
}

// Errors returns the subtask's errors, if it stopped on any.
func (s TaskStatus) Errors() []DMError {
	if s.Result == nil {
		return nil
	}
	return s.Result.Errors
}

var errTaskNotFound = errors.New("the DM task does not exist")

// Status returns the task's status, or errTaskNotFound.
func (d *DM) Status(ctx context.Context, name string) (TaskStatus, error) {
	var response struct {
		Result  bool   `json:"result"`
		Msg     string `json:"msg"`
		Sources []struct {
			Result        bool         `json:"result"`
			Msg           string       `json:"msg"`
			SubTaskStatus []TaskStatus `json:"subTaskStatus"`
		} `json:"sources"`
	}
	if err := d.call(ctx, http.MethodGet, "/apis/v1alpha1/status/"+name, nil, &response); err != nil {
		return TaskStatus{}, err
	}
	if !response.Result {
		if strings.Contains(response.Msg, "not exist") {
			return TaskStatus{}, errTaskNotFound
		}
		return TaskStatus{}, errors.New(cleanDMMessage(response.Msg))
	}
	for _, source := range response.Sources {
		if !source.Result {
			return TaskStatus{}, errors.New(cleanDMMessage(source.Msg))
		}
		for _, status := range source.SubTaskStatus {
			return status, nil
		}
	}
	return TaskStatus{}, errTaskNotFound
}

func (d *DM) call(ctx context.Context, method, path string, body, destination any) error {
	return d.callWith(ctx, d.http, method, path, body, destination)
}

func (d *DM) callWith(ctx context.Context, client *http.Client, method, path string, body, destination any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: DM is not reachable", errUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxDMResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read DM's response", errUnavailable)
	}
	// The gateway answers failed operations with a result=false body, which
	// may come with a non-200 status; decode it either way.
	if err := json.Unmarshal(payload, destination); err != nil {
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("DM returned HTTP %d", response.StatusCode)
		}
		return errors.New("DM returned an invalid response")
	}
	return nil
}

// CheckTask runs DM's precheck for the task without starting it. DM's
// gateway has no route for it, so dmctl runs it.
func (d *DM) CheckTask(ctx context.Context, config []byte) (checkResult, error) {
	file, err := os.CreateTemp("", "task-*.yaml")
	if err != nil {
		return checkResult{}, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(config); err != nil {
		file.Close()
		return checkResult{}, err
	}
	if err := file.Close(); err != nil {
		return checkResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(d.binDir, "dmctl"), "--master-addr="+dmMasterAddr, "check-task", file.Name())
	// dmctl creates a log in its working directory.
	cmd.Dir = os.TempDir()
	output, err := cmd.Output()
	if len(output) == 0 && err != nil {
		return checkResult{}, fmt.Errorf("run DM's precheck: %w", err)
	}
	return parseCheckOutput(output), nil
}

// checkResult is DM's precheck outcome.
type checkResult struct {
	Passed   bool
	Problems []string
	Warnings []string
}

// checksCoveredByAgent are DM precheck items the agent's own preflight
// reports with more specific messages.
var checksCoveredByAgent = []string{
	"mysql_binlog_enable", "mysql_binlog_format", "mysql_binlog_row_image", "mysql_server_id", "mysql_version",
}

// parseCheckOutput reads dmctl's output: a JSON-like object whose msg holds
// the check details after "detail: " (with raw newlines, so the whole output
// is not valid JSON).
func parseCheckOutput(output []byte) checkResult {
	text := string(output)
	if _, _, found := strings.Cut(text, "detail: "); found {
		return parseCheckResult(text)
	}
	var plain struct {
		Result bool   `json:"result"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(output, &plain); err == nil {
		if plain.Result {
			return checkResult{Passed: true}
		}
		return checkResult{Problems: []string{cleanDMMessage(plain.Msg)}}
	}
	if strings.Contains(text, `"result": true`) {
		return checkResult{Passed: true}
	}
	return checkResult{Problems: []string{"DM's precheck failed: " + truncate(strings.TrimSpace(text), 300)}}
}

// parseCheckResult reads a precheck message ("…detail: {json}").
func parseCheckResult(text string) checkResult {
	_, detail, found := strings.Cut(text, "detail: ")
	if !found {
		return checkResult{Problems: []string{cleanDMMessage(text)}}
	}
	var report struct {
		Results []struct {
			Name   string `json:"name"`
			State  string `json:"state"`
			Errors []struct {
				ShortError string `json:"short_error"`
			} `json:"errors"`
		} `json:"results"`
		Summary struct {
			Passed bool `json:"passed"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(strings.NewReader(detail)).Decode(&report); err != nil {
		return checkResult{Problems: []string{"DM's precheck failed: " + truncate(strings.TrimSpace(detail), 300)}}
	}
	result := checkResult{Passed: report.Summary.Passed}
	for _, item := range report.Results {
		if item.State != "fail" && item.State != "warn" {
			continue
		}
		for _, failure := range item.Errors {
			message := fmt.Sprintf("%s: %s", item.Name, truncate(strings.Join(strings.Fields(failure.ShortError), " "), 300))
			switch {
			case item.State == "warn":
				result.Warnings = append(result.Warnings, message)
			case !contains(checksCoveredByAgent, item.Name):
				result.Problems = append(result.Problems, message)
			}
		}
	}
	return result
}

// cleanDMMessage keeps the readable part of a DM error ("[code=…], Message: x").
func cleanDMMessage(message string) string {
	message = strings.TrimSpace(message)
	if _, rest, found := strings.Cut(message, "Message: "); found {
		message = rest
	}
	if before, _, found := strings.Cut(message, ", Workaround: "); found {
		message = before
	}
	return truncate(strings.Join(strings.Fields(message), " "), 500)
}

// flexNumber reads DM's numbers, which its gateway encodes as strings for
// 64-bit integers.
type flexNumber float64

func (n *flexNumber) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*n = 0
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	*n = flexNumber(value)
	return nil
}

// sourceConfig is DM's source configuration.
type sourceConfig struct {
	SourceID    string   `yaml:"source-id"`
	EnableGTID  bool     `yaml:"enable-gtid"`
	EnableRelay bool     `yaml:"enable-relay"`
	From        dbConfig `yaml:"from"`
}

type dbConfig struct {
	Host     string    `yaml:"host"`
	Port     int       `yaml:"port"`
	User     string    `yaml:"user"`
	Password string    `yaml:"password"`
	Security *security `yaml:"security,omitempty"`
}

type security struct {
	SSLCA string `yaml:"ssl-ca"`
}

func newDBConfig(endpoint Endpoint) dbConfig {
	config := dbConfig{Host: endpoint.Host, Port: endpoint.Port, User: endpoint.User, Password: endpoint.Password}
	// DM verifies the server against a CA or does not use TLS at all.
	if endpoint.VerifiesServer() {
		config.Security = &security{SSLCA: endpoint.SSLCA}
	}
	return config
}

func renderSourceConfig(name string, source Endpoint, enableGTID bool) ([]byte, error) {
	return yaml.Marshal(sourceConfig{SourceID: name, EnableGTID: enableGTID, From: newDBConfig(source)})
}

type taskConfig struct {
	Name                string                    `yaml:"name"`
	IgnoreCheckingItems []string                  `yaml:"ignore-checking-items,omitempty"`
	TaskMode            string                    `yaml:"task-mode"`
	MetaSchema          string                    `yaml:"meta-schema"`
	CollationCompatible string                    `yaml:"collation_compatible"`
	CleanDumpFile       bool                      `yaml:"clean-dump-file"`
	TargetDatabase      dbConfig                  `yaml:"target-database"`
	BlockAllowList      map[string]blockAllowList `yaml:"block-allow-list"`
	Mydumpers           map[string]map[string]any `yaml:"mydumpers"`
	Loaders             map[string]map[string]any `yaml:"loaders"`
	Syncers             map[string]map[string]any `yaml:"syncers"`
	MySQLInstances      []map[string]string       `yaml:"mysql-instances"`
}

type blockAllowList struct {
	DoDBs        []string     `yaml:"do-dbs"`
	IgnoreTables []tableMatch `yaml:"ignore-tables,omitempty"`
}

type tableMatch struct {
	DBName  string `yaml:"db-name"`
	TblName string `yaml:"tbl-name"`
}

// renderTaskConfig builds the task: a full copy followed by binlog
// replication of the databases and the heartbeat database. Only do-dbs and
// ignore-tables are used: DM skips ignore-tables for tables do-tables match.
// Collations are created as they are on the source (strict). Without
// precheck, DM starts the task without checking it again: the agent ran the
// check (CheckTask) before, and a restarted task was checked when created.
func renderTaskConfig(name string, target Endpoint, options Options, dumpDir string, precheck bool) ([]byte, error) {
	allow := blockAllowList{DoDBs: append(append([]string{}, options.Databases...), heartbeatDatabase(name))}
	for _, table := range options.ExcludeTables {
		database, tableName, _ := strings.Cut(table, ".")
		allow.IgnoreTables = append(allow.IgnoreTables, tableMatch{DBName: database, TblName: tableName})
	}
	const group = "global"
	config := taskConfig{
		Name:                name,
		IgnoreCheckingItems: map[bool][]string{false: {"all"}}[precheck],
		TaskMode:            "all",
		MetaSchema:          metaSchema,
		CollationCompatible: "strict",
		CleanDumpFile:       true,
		TargetDatabase:      newDBConfig(target),
		BlockAllowList:      map[string]blockAllowList{group: allow},
		Mydumpers:           map[string]map[string]any{group: {"threads": options.CopyThreads}},
		Loaders:             map[string]map[string]any{group: {"pool-size": options.CopyThreads, "dir": dumpDir, "import-mode": "logical"}},
		Syncers: map[string]map[string]any{group: {
			"worker-count": options.SyncThreads, "checkpoint-flush-interval": checkpointFlushSeconds,
		}},
		MySQLInstances: []map[string]string{{
			"source-id": name, "block-allow-list": group,
			"mydumper-config-name": group, "loader-config-name": group, "syncer-config-name": group,
		}},
	}
	return yaml.Marshal(config)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
