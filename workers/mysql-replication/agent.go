package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Agent states reported by GET /status.
const (
	StateIdle       = "idle"
	StateRunning    = "running"
	StatePaused     = "paused"
	StateFinalizing = "finalizing"
	StateCompleted  = "completed"
	StateFailed     = "failed"
	StateStopped    = "stopped"
)

const (
	sqlTimeout = 30 * time.Second
	// caughtUpLag marks the initial sync complete: the target applies changes
	// close enough to real time for a cutover to be quick.
	caughtUpLag         = 15 * time.Second
	cutoverPollInterval = time.Second
	maxListedTables     = 10
	// Target 8.0.23 has CHANGE REPLICATION SOURCE and DISABLE ON REPLICA.
	minSourceVersion = 80000
	minTargetVersion = 80023
)

// Phases stored in the target's state table.
const (
	phaseCopying     = "copying"
	phaseReplicating = "replicating"
	phaseCompleted   = "completed"
)

var (
	errConflict    = errors.New("conflict")
	errInvalidBody = errors.New("invalid request")
	errUnavailable = errors.New("unavailable")
)

// Config comes from the worker's environment; endpoints carry credentials.
type Config struct {
	Source            Endpoint
	Target            Endpoint
	Name              string
	WorkDir           string
	HeartbeatInterval time.Duration
}

// Status is the GET /status response body.
type Status struct {
	State                string `json:"state"`
	Message              string `json:"message,omitempty"`
	Error                string `json:"error,omitempty"`
	CopyDone             bool   `json:"copyDone"`
	InitialSyncCompleted bool   `json:"initialSyncCompleted"`
	BytesTotal           uint64 `json:"bytesTotal"`
	BytesCopied          uint64 `json:"bytesCopied"`
	LagSeconds           int64  `json:"lagSeconds"`
}

type stage int

const (
	// stageRecovering lasts until the target's state table has been read.
	stageRecovering stage = iota
	stageIdle
	stagePreparing
	stageReplicating
	stageCutover
	stageCompleted
	stageStopped
)

type Agent struct {
	config Config

	// controlMu serializes everything that starts or stops the channel
	// (supervision, pause, resume, cutover), so the supervisor never acts on
	// a channel state that a concurrent pause just changed. Lock it before mu.
	controlMu sync.Mutex
	mu        sync.Mutex
	stage     stage
	step      string
	failure   string
	cancel    context.CancelFunc
	options   Options
	// replica is the target server the channel runs on. On PXC that is one
	// node: the GTIDs of the copy are recorded only there.
	replica     Endpoint
	paused      bool
	initialSync bool
	// channelGone is set once the cutover removed the channel.
	channelGone bool
	bytesTotal  uint64
	bytesCopied uint64
	lastStart   time.Time
	// notes tell the user what the cutover could not finish.
	notes string

	dbMu sync.Mutex
	dbs  map[string]*sql.DB
}

func NewAgent(config Config) *Agent {
	return &Agent{config: config, replica: config.Target, dbs: map[string]*sql.DB{}}
}

// db returns a connection pool for endpoint, shared by address.
func (a *Agent) db(endpoint Endpoint) (*sql.DB, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	key := endpoint.User + "@" + endpoint.Address()
	if db, ok := a.dbs[key]; ok {
		return db, nil
	}
	db, err := openDB(endpoint)
	if err != nil {
		return nil, err
	}
	a.dbs[key] = db
	return db, nil
}

func (a *Agent) source() (*sql.DB, error) { return a.db(a.config.Source) }
func (a *Agent) target() (*sql.DB, error) { return a.db(a.config.Target) }

func (a *Agent) replicaDB() (*sql.DB, error) {
	a.mu.Lock()
	replica := a.replica
	a.mu.Unlock()
	return a.db(replica)
}

func (a *Agent) setStep(step string) {
	a.mu.Lock()
	a.step = step
	a.mu.Unlock()
	log.Print(step)
}

// RecoverLoop restores the stage after a worker restart, retrying until the
// target answers: until then the agent cannot tell whether a migration runs,
// so it neither supervises nor accepts a start.
func (a *Agent) RecoverLoop(ctx context.Context) {
	delay := time.Second
	for !a.Recover(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, time.Minute)
	}
}

// Recover reads the target's state table once. It reports false when the
// target could not be read. Replication itself keeps running meanwhile.
func (a *Agent) Recover(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	target, err := a.target()
	if err != nil {
		log.Printf("recover: %v", err)
		return false
	}
	var tables int
	if err := target.QueryRowContext(ctx, migrationTableExistsSQL, "state").Scan(&tables); err != nil {
		log.Printf("recover: read the target: %s", serverError(err))
		return false
	}
	var (
		address, rawOptions, phase, localInfile, notes string
		paused                                         bool
		copied                                         uint64
	)
	found := false
	if tables > 0 {
		err = target.QueryRowContext(ctx, readStateSQL, a.config.Name).Scan(&address, &rawOptions, &phase, &paused, &localInfile, &copied, &notes)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("recover: read the migration state: %s", serverError(err))
			return false
		}
		found = err == nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageRecovering {
		return true // torn down meanwhile
	}
	if !found {
		a.stage = stageIdle
		return true
	}
	options, err := decodeOptions(json.RawMessage(rawOptions))
	if err == nil {
		a.replica, err = a.config.Target.WithAddress(address)
	}
	if err != nil {
		a.stage, a.failure = stagePreparing, "The migration state on the target cannot be read: "+err.Error()
		return true
	}
	a.options, a.paused, a.notes = options, paused, notes
	a.bytesTotal, a.bytesCopied = copied, copied
	switch phase {
	case phaseReplicating:
		a.stage = stageReplicating
		log.Printf("recovered a running migration (replicating on %s)", a.replica.Address())
	case phaseCompleted:
		a.stage, a.initialSync, a.channelGone = stageCompleted, true, true
	default:
		a.stage = stagePreparing
		a.failure = "The migration worker restarted before the copy finished. Delete this migration and create it again with \"Recreate the target databases\" enabled."
	}
	return true
}

// Start copies the data and starts replication in the background. Calling it
// again after the first call does nothing.
func (a *Agent) Start(options Options) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageRecovering {
		return fmt.Errorf("%w: the worker is still reading the migration state from the target", errUnavailable)
	}
	if a.stage != stageIdle {
		return nil // the reconciler retries Start after a lost response
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel, a.options = stagePreparing, cancel, options
	go func() {
		err := a.prepare(ctx, options)
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Printf("start: %v", err)
			a.failure = err.Error()
		default:
			a.stage, a.step, a.lastStart = stageReplicating, "", time.Now()
		}
	}()
	return nil
}

// serverInfo is what preflight needs from SHOW GLOBAL VARIABLES.
type serverInfo struct {
	Version          string
	Comment          string
	UUID             string
	LogBin           bool
	GTIDMode         string
	BinlogFormat     string
	SkipReplicaStart bool
	LocalInfile      bool
	Galera           bool
	NodeAddress      string
}

func newServerInfo(variables map[string]string) serverInfo {
	on := func(name string) bool {
		value := strings.ToUpper(variables[name])
		return value == "ON" || value == "1"
	}
	info := serverInfo{
		Version: variables["version"], Comment: variables["version_comment"], UUID: variables["server_uuid"],
		LogBin: on("log_bin"), GTIDMode: strings.ToUpper(variables["gtid_mode"]), BinlogFormat: strings.ToUpper(variables["binlog_format"]),
		SkipReplicaStart: on("skip_replica_start"), LocalInfile: on("local_infile"),
		Galera: variables["wsrep_provider"] != "" && !strings.EqualFold(variables["wsrep_provider"], "none"),
	}
	if address := variables["wsrep_node_incoming_address"]; info.Galera && address != "" && !strings.EqualFold(address, "AUTO") {
		info.NodeAddress = address
	}
	return info
}

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// versionNumber turns "8.0.46-37" into 80046; 0 if it cannot be read.
func versionNumber(version string) int {
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return major*10000 + minor*100 + patch
}

func isMariaDB(info serverInfo) bool {
	return strings.Contains(strings.ToLower(info.Version+" "+info.Comment), "mariadb")
}

type sourceFacts struct {
	Info         serverInfo
	CanReplicate bool
	Missing      []string
	// Unsupported lists tables PXC cannot replicate (no primary key or not InnoDB).
	Unsupported []string
	DataBytes   uint64
}

type targetFacts struct {
	Info          serverInfo
	Existing      []string
	ChannelExists bool
}

func (a *Agent) readSource(ctx context.Context, options Options) (sourceFacts, error) {
	db, err := a.source()
	if err != nil {
		return sourceFacts{}, err
	}
	variables, err := serverVariables(ctx, db)
	if err != nil {
		return sourceFacts{}, err
	}
	facts := sourceFacts{Info: newServerInfo(variables)}
	facts.CanReplicate = canReplicate(ctx, db)
	query, args := inList(existingDatabasesSQL, options.Databases)
	existing, err := queryStrings(ctx, db, query, args...)
	if err != nil {
		return sourceFacts{}, err
	}
	for _, database := range options.Databases {
		if !slices.Contains(existing, database) {
			facts.Missing = append(facts.Missing, database)
		}
	}
	query, args = inList(sourceTablesSQL, options.Databases)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return sourceFacts{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			database, table, engine string
			size                    uint64
			hasPrimaryKey           bool
		)
		if err := rows.Scan(&database, &table, &engine, &size, &hasPrimaryKey); err != nil {
			return sourceFacts{}, err
		}
		if slices.Contains(options.ExcludeTables, database+"."+table) {
			continue
		}
		facts.DataBytes += size
		if !hasPrimaryKey || !strings.EqualFold(engine, "InnoDB") {
			facts.Unsupported = append(facts.Unsupported, database+"."+table)
		}
	}
	return facts, rows.Err()
}

// canReplicate probes the REPLICATION SLAVE privilege (also when granted
// through a role): SHOW REPLICAS requires it.
func canReplicate(ctx context.Context, db *sql.DB) bool {
	for _, statement := range []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"} {
		rows, err := db.QueryContext(ctx, statement)
		if err == nil {
			rows.Close()
			return true
		}
		if !isServerError(err, errSyntax) {
			return false
		}
	}
	return false
}

func (a *Agent) readTarget(ctx context.Context, options Options) (targetFacts, error) {
	db, err := a.target()
	if err != nil {
		return targetFacts{}, err
	}
	variables, err := serverVariables(ctx, db)
	if err != nil {
		return targetFacts{}, err
	}
	facts := targetFacts{Info: newServerInfo(variables)}
	query, args := inList(existingDatabasesSQL, options.Databases)
	if facts.Existing, err = queryStrings(ctx, db, query, args...); err != nil {
		return targetFacts{}, err
	}
	channel, err := readChannel(ctx, db, a.config.Name)
	if err != nil {
		return targetFacts{}, err
	}
	facts.ChannelExists = channel.Exists
	return facts, nil
}

// checkPreflight lists every problem at once so they can be fixed together.
func checkPreflight(source sourceFacts, target targetFacts, options Options) error {
	var problems []string
	sourceVersion, targetVersion := versionNumber(source.Info.Version), versionNumber(target.Info.Version)
	switch {
	case isMariaDB(source.Info):
		problems = append(problems, "the source is MariaDB; only MySQL-compatible sources (MySQL, Percona Server, PXC) are supported")
	case sourceVersion < minSourceVersion:
		problems = append(problems, fmt.Sprintf("the source runs MySQL %s; version 8.0 or later is required", source.Info.Version))
	}
	if !source.Info.LogBin {
		problems = append(problems, "binary logging is disabled on the source; enable log_bin")
	}
	if source.Info.GTIDMode != "ON" {
		problems = append(problems, fmt.Sprintf("the source has gtid_mode=%s; set gtid_mode=ON and enforce_gtid_consistency=ON", orUnknown(source.Info.GTIDMode)))
	}
	if source.Info.BinlogFormat != "ROW" {
		problems = append(problems, fmt.Sprintf("the source has binlog_format=%s; set binlog_format=ROW", orUnknown(source.Info.BinlogFormat)))
	}
	if !source.CanReplicate {
		problems = append(problems, "the source user needs the REPLICATION SLAVE privilege")
	}
	if len(source.Missing) > 0 {
		problems = append(problems, "databases not found on the source: "+strings.Join(source.Missing, ", "))
	}
	if target.Info.Galera && len(source.Unsupported) > 0 {
		problems = append(problems, "PXC replicates only InnoDB tables with a primary key; add a primary key, convert to InnoDB or exclude: "+
			listNames(source.Unsupported))
	}
	if targetVersion < minTargetVersion {
		problems = append(problems, fmt.Sprintf("the target runs MySQL %s; version 8.0.23 or later is required", target.Info.Version))
	} else if targetVersion/100 < sourceVersion/100 {
		problems = append(problems, fmt.Sprintf("the target runs MySQL %s, older than the source (%s); migrate to the same or a newer version",
			target.Info.Version, source.Info.Version))
	}
	if target.Info.GTIDMode != "ON" {
		problems = append(problems, fmt.Sprintf("the target has gtid_mode=%s; GTID mode must be ON", orUnknown(target.Info.GTIDMode)))
	}
	if !target.Info.SkipReplicaStart {
		problems = append(problems, "the target must run with skip_replica_start=ON, so that a restarted server does not replicate before the migration worker restores its filters")
	}
	if source.Info.UUID != "" && source.Info.UUID == target.Info.UUID {
		problems = append(problems, "the source and the target are the same server")
	}
	if len(target.Existing) > 0 && !options.RecreateDatabases {
		problems = append(problems, "these databases already exist on the target: "+strings.Join(target.Existing, ", ")+
			"; drop them or enable \"Recreate the target databases\"")
	}
	if target.ChannelExists {
		problems = append(problems, "the target already has a replication channel for this migration; delete the migration and create it again")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("Cannot start the migration: " + strings.Join(problems, "; ") + ".")
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func listNames(names []string) string {
	if len(names) <= maxListedTables {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxListedTables], ", "), len(names)-maxListedTables)
}

func (a *Agent) prepare(ctx context.Context, options Options) error {
	a.setStep("Checking the source and target")
	checkCtx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	source, err := a.readSource(checkCtx, options)
	if err != nil {
		cancel()
		return fmt.Errorf("connect to the source: %s", serverError(err))
	}
	target, err := a.readTarget(checkCtx, options)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to the target: %s", serverError(err))
	}
	if err := checkPreflight(source, target, options); err != nil {
		return err
	}

	// On PXC the channel and the copy's GTIDs live on one node; talk to it
	// directly so that a proxy failover cannot split them.
	replica := a.config.Target
	if target.Info.NodeAddress != "" {
		if replica, err = a.config.Target.WithAddress(target.Info.NodeAddress); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.replica, a.bytesTotal = replica, source.DataBytes
	a.mu.Unlock()

	a.setStep("Preparing the source and target")
	if err := a.prepareServers(ctx, options, replica, target.Info.LocalInfile); err != nil {
		return err
	}
	policy, remapped, err := a.readDefiners(ctx, options)
	if err != nil {
		return err
	}

	a.setStep("Copying data")
	gtids, err := a.copy(ctx, options, replica, target.Info.LocalInfile, copyExclusions(remapped))
	if err != nil {
		return err
	}
	if len(remapped) > 0 {
		a.setStep(fmt.Sprintf("Creating %d object(s) whose definer the target user cannot assign", len(remapped)))
		if err := a.recreate(ctx, remapped); err != nil {
			return err
		}
	}

	a.setStep("Starting replication")
	return a.startReplication(ctx, options, gtids, policy)
}

func (a *Agent) prepareServers(ctx context.Context, options Options, replica Endpoint, localInfile bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	replicaDB, err := a.db(replica)
	if err != nil {
		return err
	}
	sourceDB, err := a.source()
	if err != nil {
		return err
	}
	encodedOptions, err := json.Marshal(options)
	if err != nil {
		return err
	}
	originalLocalInfile := "OFF"
	if localInfile {
		originalLocalInfile = "ON"
	}
	for _, statement := range []struct {
		db   *sql.DB
		what string
		sql  string
		args []any
	}{
		{replicaDB, "create the migration database on the target", createMigrationDatabaseSQL, nil},
		{replicaDB, "create the state table on the target", createStateTableSQL, nil},
		{replicaDB, "record the migration on the target", insertStateSQL, []any{a.config.Name, replica.Address(), string(encodedOptions), originalLocalInfile}},
		{replicaDB, "create the heartbeat table on the target", createHeartbeatTableSQL(a.config.Name), nil},
		{replicaDB, "seed the heartbeat on the target", seedHeartbeatSQL(a.config.Name), []any{a.config.Name}},
		// The source row must exist before the copy's snapshot: replicated as
		// an insert, it would collide with the seeded target row.
		{sourceDB, "create the migration database on the source", createMigrationDatabaseSQL, nil},
		{sourceDB, "create the heartbeat table on the source", createHeartbeatTableSQL(a.config.Name), nil},
		{sourceDB, "write the heartbeat on the source", beatSQL(a.config.Name), []any{a.config.Name}},
	} {
		if _, err := statement.db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return fmt.Errorf("%s: %s", statement.what, serverError(err))
		}
	}
	if options.RecreateDatabases {
		for _, database := range options.Databases {
			if _, err := replicaDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(database)); err != nil {
				return fmt.Errorf("drop database %s on the target: %s", database, serverError(err))
			}
		}
	}
	return nil
}

// readDefiners finds the objects the copy must leave out because their
// definer holds SYSTEM_USER on the target and the agent's user does not.
func (a *Agent) readDefiners(ctx context.Context, options Options) (definerPolicy, []definedObject, error) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	replicaDB, err := a.replicaDB()
	if err != nil {
		return definerPolicy{}, nil, err
	}
	policy, err := readDefinerPolicy(ctx, replicaDB)
	if err != nil {
		return definerPolicy{}, nil, fmt.Errorf("read the target's privileged accounts: %s", serverError(err))
	}
	sourceDB, err := a.source()
	if err != nil {
		return definerPolicy{}, nil, err
	}
	objects, err := policy.objects(ctx, sourceDB, options.Databases)
	if err != nil {
		return definerPolicy{}, nil, fmt.Errorf("list views, routines, triggers and events on the source: %s", serverError(err))
	}
	for _, object := range objects {
		log.Printf("%s %s.%s is defined by %s, which the target user cannot assign; it will be defined by the target user",
			strings.ToLower(object.Kind), object.Database, object.Name, object.Definer)
	}
	return policy, objects, nil
}

func (a *Agent) recreate(ctx context.Context, objects []definedObject) error {
	ctx, cancel := context.WithTimeout(ctx, 5*sqlTimeout)
	defer cancel()
	sourceDB, err := a.source()
	if err != nil {
		return err
	}
	replicaDB, err := a.replicaDB()
	if err != nil {
		return err
	}
	return recreateObjects(ctx, sourceDB, replicaDB, objects)
}

// copy loads the databases into the replica node. MySQL Shell loads with
// LOAD DATA LOCAL INFILE, so local_infile is enabled for the copy only.
func (a *Agent) copy(ctx context.Context, options Options, replica Endpoint, localInfile bool, exclusions map[string][]string) (string, error) {
	replicaDB, err := a.db(replica)
	if err != nil {
		return "", err
	}
	if !localInfile {
		if _, err := replicaDB.ExecContext(ctx, setLocalInfileSQL, "ON"); err != nil {
			return "", fmt.Errorf("enable local_infile on the target: %s", serverError(err))
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sqlTimeout)
			defer cancel()
			if _, err := replicaDB.ExecContext(restoreCtx, setLocalInfileSQL, "OFF"); err != nil {
				log.Printf("restore local_infile on the target: %s", serverError(err))
			}
		}()
	}
	if err := os.MkdirAll(a.config.WorkDir, 0o700); err != nil {
		return "", fmt.Errorf("create the work directory: %w", err)
	}
	return runCopy(ctx, a.config.WorkDir, a.config.Source, replica, options, exclusions, func(loaded uint64) {
		a.mu.Lock()
		a.bytesCopied = loaded
		if loaded > a.bytesTotal {
			a.bytesTotal = loaded
		}
		a.mu.Unlock()
	})
}

func (a *Agent) startReplication(ctx context.Context, options Options, gtids string, policy definerPolicy) error {
	ctx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	replicaDB, err := a.replicaDB()
	if err != nil {
		return err
	}
	if err := disableCopiedEvents(ctx, replicaDB, options, policy); err != nil {
		return err
	}
	var missing string
	if err := replicaDB.QueryRowContext(ctx, missingGTIDsSQL, gtids).Scan(&missing); err != nil {
		return fmt.Errorf("compare GTID sets on the target: %s", serverError(err))
	}
	if missing = strings.Join(strings.Fields(missing), ""); missing != "" {
		if _, err := replicaDB.ExecContext(ctx, appendGTIDPurgedSQL, missing); err != nil {
			return fmt.Errorf("record the copied transactions on the target (gtid_purged): %s", serverError(err))
		}
	}
	source := a.config.Source
	useTLS := 0
	if source.UsesTLS() {
		useTLS = 1
	}
	if _, err := replicaDB.ExecContext(ctx, changeSourceSQL, source.Host, source.Port, source.User, source.Password, useTLS, a.config.Name); err != nil {
		return fmt.Errorf("configure replication on the target: %s", serverError(err))
	}
	if err := startChannel(ctx, replicaDB, a.config.Name, options); err != nil {
		return err
	}
	a.mu.Lock()
	copied := a.bytesCopied
	a.bytesTotal = copied
	a.mu.Unlock()
	if _, err := replicaDB.ExecContext(ctx, setPhaseSQL, phaseReplicating, copied, a.config.Name); err != nil {
		return fmt.Errorf("record the migration on the target: %s", serverError(err))
	}
	return nil
}

// disableCopiedEvents keeps copied scheduled events from running on the
// target while it replicates; replicated events arrive disabled already.
// The cutover enables the events that are enabled on the source.
func disableCopiedEvents(ctx context.Context, db *sql.DB, options Options, policy definerPolicy) error {
	query, args := inList(enabledEventsSQL, options.Databases)
	events, err := readEvents(ctx, db, query, args, false)
	if err != nil {
		return fmt.Errorf("list events on the target: %s", serverError(err))
	}
	for _, event := range events {
		if err := alterEvent(ctx, db, event, "DISABLE ON REPLICA", policy); err != nil {
			return err
		}
	}
	return nil
}

type scheduledEvent struct {
	Database, Name, Definer, Status string
}

func readEvents(ctx context.Context, db *sql.DB, query string, args []any, withStatus bool) ([]scheduledEvent, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []scheduledEvent
	for rows.Next() {
		var event scheduledEvent
		destinations := []any{&event.Database, &event.Name, &event.Definer}
		if withStatus {
			destinations = append(destinations, &event.Status)
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// alterEvent changes an event's status. ALTER EVENT would make the agent's
// user the definer, so the original definer is kept explicitly, unless the
// agent's user cannot assign it (see definerPolicy).
func alterEvent(ctx context.Context, db *sql.DB, event scheduledEvent, status string, policy definerPolicy) error {
	name := quoteIdentifier(event.Database) + "." + quoteIdentifier(event.Name)
	at := strings.LastIndex(event.Definer, "@")
	statement, args := "ALTER DEFINER = ?@? EVENT "+name+" "+status, []any{}
	switch {
	case policy.remaps(event.Definer):
		statement = "ALTER DEFINER = CURRENT_USER EVENT " + name + " " + status
	case at < 0:
		return fmt.Errorf("event %s.%s has an unexpected definer", event.Database, event.Name)
	default:
		args = append(args, event.Definer[:at], event.Definer[at+1:])
	}
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		return fmt.Errorf("update event %s.%s on the target: %w", event.Database, event.Name, err)
	}
	return nil
}

// eventStatusChange returns the ALTER EVENT clause that gives a target event
// the status it has on the source, or "" if it already has it. Events that
// replicated or were disabled after the copy are disabled "on replica".
func eventStatusChange(event scheduledEvent, sourceEvents []scheduledEvent) string {
	enabledOnSource := slices.ContainsFunc(sourceEvents, func(source scheduledEvent) bool {
		return source.Database == event.Database && source.Name == event.Name && source.Status == "ENABLED"
	})
	switch {
	case enabledOnSource && event.Status != "ENABLED":
		return "ENABLE"
	case !enabledOnSource && event.Status != "DISABLED":
		return "DISABLE"
	}
	return ""
}

func (a *Agent) Pause(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageReplicating || a.failure != "" || a.paused {
		return fmt.Errorf("%w: changes can be paused only while they are replicated", errConflict)
	}
	db, err := a.db(a.replica)
	if err != nil {
		return err
	}
	if err := channelStatement(ctx, db, "STOP REPLICA", a.config.Name); err != nil {
		return fmt.Errorf("stop replication: %s", serverError(err))
	}
	if _, err := db.ExecContext(ctx, setPausedSQL, true, a.config.Name); err != nil {
		return fmt.Errorf("record the pause on the target: %s", serverError(err))
	}
	a.paused = true
	return nil
}

// Resume starts a paused channel, or one that stopped on an error after the
// cause was fixed.
func (a *Agent) Resume(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageReplicating || a.failure != "" {
		return fmt.Errorf("%w: only a replicating migration can be resumed", errConflict)
	}
	db, err := a.db(a.replica)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, setPausedSQL, false, a.config.Name); err != nil {
		return fmt.Errorf("record the resume on the target: %s", serverError(err))
	}
	a.paused, a.lastStart = false, time.Now()
	return startChannel(ctx, db, a.config.Name, a.options)
}

// Cutover waits, in the background, until the target applied every
// transaction committed on the source before the request, then removes the
// channel and finalizes the target.
func (a *Agent) Cutover(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageCutover {
		return nil
	}
	if a.stage != stageReplicating || !a.initialSync || a.failure != "" {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	sourceDB, err := a.db(a.config.Source)
	if err != nil {
		return err
	}
	var gtids string
	if err := sourceDB.QueryRowContext(ctx, gtidExecutedSQL).Scan(&gtids); err != nil {
		return fmt.Errorf("read the source position: %s", serverError(err))
	}
	if a.paused {
		db, err := a.db(a.replica)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, setPausedSQL, false, a.config.Name); err != nil {
			return fmt.Errorf("record the resume on the target: %s", serverError(err))
		}
		if err := startChannel(ctx, db, a.config.Name, a.options); err != nil {
			return err
		}
		a.paused = false
	}
	cutoverCtx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel = stageCutover, cancel
	go func() {
		err := a.cutover(cutoverCtx, gtids)
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case cutoverCtx.Err() != nil:
		case err != nil:
			log.Printf("cutover: %v", err)
			a.failure = "Cutover failed: " + err.Error()
		default:
			a.stage = stageCompleted
			log.Print("migration completed")
		}
	}()
	return nil
}

func (a *Agent) cutover(ctx context.Context, gtids string) error {
	replicaDB, err := a.replicaDB()
	if err != nil {
		return err
	}
	for {
		var timedOut sql.NullInt64
		err := replicaDB.QueryRowContext(ctx, waitForGTIDsSQL, gtids, 5).Scan(&timedOut)
		if err == nil && timedOut.Valid && timedOut.Int64 == 0 {
			break
		}
		if err != nil {
			log.Printf("cutover: wait for the target: %s", serverError(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cutoverPollInterval):
		}
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	a.channelGone = true
	options := a.options
	a.mu.Unlock()

	sqlCtx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	sourceDB, err := a.source()
	if err != nil {
		return err
	}
	query, args := inList(targetEventsSQL, options.Databases)
	sourceEvents, err := readEvents(sqlCtx, sourceDB, query, args, true)
	if err != nil {
		return fmt.Errorf("list events on the source: %s", serverError(err))
	}
	if err := removeChannel(sqlCtx, replicaDB, a.config.Name); err != nil {
		return fmt.Errorf("remove the replication channel: %w", err)
	}
	targetEvents, err := readEvents(sqlCtx, replicaDB, query, args, true)
	if err != nil {
		return fmt.Errorf("list events on the target: %s", serverError(err))
	}
	policy, err := readDefinerPolicy(sqlCtx, replicaDB)
	if err != nil {
		return fmt.Errorf("read the target's privileged accounts: %s", serverError(err))
	}
	var manual []string
	for _, event := range targetEvents {
		change := eventStatusChange(event, sourceEvents)
		if change == "" {
			continue
		}
		err := alterEvent(sqlCtx, replicaDB, event, change, policy)
		var denied *mysql.MySQLError
		switch {
		case errors.As(err, &denied) && denied.Number == errAccessDenied:
			// Created by replication with a definer only SYSTEM_USER may
			// change; the agent's user can neither alter nor drop it.
			manual = append(manual, fmt.Sprintf("ALTER EVENT %s.%s %s (defined by %s)",
				quoteIdentifier(event.Database), quoteIdentifier(event.Name), change, event.Definer))
		case err != nil:
			return err
		}
	}
	notes := ""
	if len(manual) > 0 {
		notes = "Run as a user with SYSTEM_USER on the target: " + strings.Join(manual, "; ") + "."
	}
	a.cleanupSource(sqlCtx)
	a.mu.Lock()
	copied := a.bytesCopied
	a.mu.Unlock()
	if _, err := replicaDB.ExecContext(sqlCtx, setCompletedSQL, copied, notes, a.config.Name); err != nil {
		return fmt.Errorf("record the completion on the target: %s", serverError(err))
	}
	a.mu.Lock()
	a.notes = notes
	a.mu.Unlock()
	if _, err := replicaDB.ExecContext(sqlCtx, dropHeartbeatSQL(a.config.Name)); err != nil {
		log.Printf("drop the heartbeat table on the target: %s", serverError(err))
	}
	return nil
}

// cleanupSource drops this migration's heartbeat table, and the migration
// database once no migration uses it. Neither statement matches another
// migration's filters, which name only that migration's own table.
func (a *Agent) cleanupSource(ctx context.Context) {
	db, err := a.source()
	if err != nil {
		log.Printf("clean up the source: %v", err)
		return
	}
	if _, err := db.ExecContext(ctx, dropHeartbeatSQL(a.config.Name)); err != nil {
		log.Printf("remove the heartbeat on the source: %s", serverError(err))
		return
	}
	var remaining int
	if err := db.QueryRowContext(ctx, countMigrationTablesSQL).Scan(&remaining); err == nil && remaining == 0 {
		if _, err := db.ExecContext(ctx, dropMigrationDatabaseSQL); err != nil {
			log.Printf("drop the migration database on the source: %s", serverError(err))
		}
	}
}

// Teardown stops background work and removes everything the migration
// created on the source and target except the migrated data. Idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	// Without the state, the channel's node is unknown.
	a.mu.Lock()
	recovering := a.stage == stageRecovering
	a.mu.Unlock()
	if recovering && !a.Recover(ctx) {
		return errors.New("cannot reach the target to remove the replication channel")
	}
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.stage = stageStopped
	replica := a.replica
	a.mu.Unlock()

	db, err := a.db(replica)
	if err != nil {
		return err
	}
	if err := removeChannel(ctx, db, a.config.Name); err != nil {
		return fmt.Errorf("remove the replication channel on the target: %w", err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, migrationTableExistsSQL, "state").Scan(&tables); err != nil {
		return fmt.Errorf("read the target: %s", serverError(err))
	}
	if tables > 0 {
		var (
			address, rawOptions, phase, localInfile, notes string
			paused                                         bool
			copied                                         uint64
		)
		err := db.QueryRowContext(ctx, readStateSQL, a.config.Name).Scan(&address, &rawOptions, &phase, &paused, &localInfile, &copied, &notes)
		if err == nil && phase == phaseCopying && localInfile == "OFF" {
			if _, err := db.ExecContext(ctx, setLocalInfileSQL, "OFF"); err != nil {
				log.Printf("restore local_infile on the target: %s", serverError(err))
			}
		}
	}
	if _, err := db.ExecContext(ctx, dropMigrationDatabaseSQL); err != nil {
		return fmt.Errorf("remove the migration database on the target: %s", serverError(err))
	}
	a.cleanupSource(ctx)
	return nil
}

// RunSupervisor writes the source heartbeat and keeps the channel running:
// after a target restart the channel is stopped and its filters are gone.
func (a *Agent) RunSupervisor(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		current, paused, gone, failure := a.stage, a.paused, a.channelGone, a.failure
		a.mu.Unlock()
		if failure != "" || (current != stageReplicating && current != stageCutover) || gone {
			continue
		}
		tickCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
		if db, err := a.source(); err == nil {
			if _, err := db.ExecContext(tickCtx, beatSQL(a.config.Name), a.config.Name); err != nil {
				log.Printf("heartbeat: %s", serverError(err))
			}
		}
		if !paused {
			a.supervise(tickCtx)
		}
		cancel()
	}
}

// supervise checks the channel once. The state is read again under
// controlMu: a pause, resume or cutover may have happened since the tick.
func (a *Agent) supervise(ctx context.Context) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	current, paused, gone, failure := a.stage, a.paused, a.channelGone, a.failure
	a.mu.Unlock()
	if paused || gone || failure != "" || (current != stageReplicating && current != stageCutover) {
		return
	}
	a.superviseChannel(ctx)
}

func (a *Agent) superviseChannel(ctx context.Context) {
	db, err := a.replicaDB()
	if err != nil {
		return
	}
	channel, err := readChannel(ctx, db, a.config.Name)
	if err != nil {
		log.Printf("supervise: read the channel: %s", serverError(err))
		return
	}
	a.mu.Lock()
	options, sinceStart := a.options, time.Since(a.lastStart)
	a.mu.Unlock()
	switch planSupervision(channel, options, a.config.Name, sinceStart) {
	case superviseStart:
		log.Printf("supervise: starting replication (io=%s sql=%s)", channel.IORunning, channel.SQLRunning)
		a.mu.Lock()
		a.lastStart = time.Now()
		a.mu.Unlock()
		if err := startChannel(ctx, db, a.config.Name, options); err != nil {
			log.Printf("supervise: %v", err)
		}
	case superviseSkip:
		failure, ok := readWorkerError(ctx, db, a.config.Name)
		if !ok {
			return
		}
		database, skippable := skippableDatabase(failure, options)
		if !skippable {
			return
		}
		log.Printf("supervise: skipping transaction %s: it changes database %q, which is not migrated", failure.Transaction, database)
		if err := channelStatement(ctx, db, "STOP REPLICA", a.config.Name); err != nil {
			log.Printf("supervise: stop replication: %s", serverError(err))
			return
		}
		if err := skipTransaction(ctx, db, failure.Transaction); err != nil {
			log.Printf("supervise: skip %s: %s", failure.Transaction, serverError(err))
		}
		if err := startChannel(ctx, db, a.config.Name, options); err != nil {
			log.Printf("supervise: %v", err)
		}
	}
}

func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	current, step, failure := a.stage, a.step, a.failure
	initialSync, paused, gone, notes := a.initialSync, a.paused, a.channelGone, a.notes
	total, copied := a.bytesTotal, a.bytesCopied
	a.mu.Unlock()

	switch {
	case current == stageRecovering:
		return Status{State: StateStopped, Message: "Connecting to the target to read the migration state"}
	case current == stageIdle:
		return Status{State: StateIdle}
	case current == stageStopped:
		return Status{State: StateStopped, Message: "Stopped"}
	case current == stageCompleted:
		return Status{State: StateCompleted, Message: strings.TrimSpace("Migration completed. " + notes), CopyDone: true, InitialSyncCompleted: true,
			BytesTotal: copied, BytesCopied: copied}
	case current == stagePreparing && failure != "":
		return Status{State: StateFailed, Error: failure, BytesTotal: total, BytesCopied: copied}
	case current == stagePreparing:
		return Status{State: StateRunning, Message: step, BytesTotal: total, BytesCopied: copied}
	case current == stageCutover && gone:
		status := Status{State: StateFinalizing, Message: "Finalizing the target", CopyDone: true, InitialSyncCompleted: true, BytesTotal: copied, BytesCopied: copied}
		if failure != "" {
			status.State, status.Error = StateFailed, failure
		}
		return status
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snap, err := a.readSnapshot(ctx)
	if err != nil {
		return Status{State: StateRunning, CopyDone: true, InitialSyncCompleted: initialSync, BytesTotal: copied, BytesCopied: copied, Error: err.Error()}
	}
	snap.Paused = paused
	status := deriveStatus(current == stageCutover, failure, snap, initialSync, copied)
	a.mu.Lock()
	a.initialSync = status.InitialSyncCompleted
	a.mu.Unlock()
	return status
}

func (a *Agent) readSnapshot(ctx context.Context) (snapshot, error) {
	var snap snapshot
	replicaDB, err := a.replicaDB()
	if err != nil {
		return snap, err
	}
	if snap.Channel, err = readChannel(ctx, replicaDB, a.config.Name); err != nil {
		return snap, fmt.Errorf("Could not read the target: %s", serverError(err))
	}
	if snap.Channel.SQLErrno != 0 {
		snap.Worker, _ = readWorkerError(ctx, replicaDB, a.config.Name)
	}
	var beatAt time.Time
	if err := replicaDB.QueryRowContext(ctx, readHeartbeatSQL(a.config.Name), a.config.Name).Scan(&beatAt); err == nil {
		if beatAt.Year() > 1970 {
			snap.BeatAt = &beatAt
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return snap, fmt.Errorf("Could not read the target: %s", serverError(err))
	}
	sourceDB, err := a.source()
	if err != nil {
		return snap, err
	}
	if err := sourceDB.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&snap.SourceNow); err != nil {
		return snap, fmt.Errorf("Could not read the source: %s", serverError(err))
	}
	return snap, nil
}
