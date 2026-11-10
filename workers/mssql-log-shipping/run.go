package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// stripeBytes keeps every stripe of a full backup well under the 10,000
	// parts S3 allows at SQL Server's default 10 MiB part size (about 97
	// GiB). Larger parts (MAXTRANSFERSIZE) fail against some S3 servers.
	stripeBytes = 75 << 30
	maxStripes  = 64
)

// run drives the migration until it completes, fails or is torn down.
func (a *Agent) run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		a.mu.Lock()
		phase, paused, interval := a.phase, a.paused, time.Duration(a.options.LogBackupIntervalSeconds)*time.Second
		a.mu.Unlock()
		started := time.Now()
		var (
			err   error
			moved bool
		)
		if phase == phaseCopying || phase == phaseCutover || (phase == phaseShipping && !paused) {
			// A move restarts the copy: the phase changed under this pass.
			moved, err = a.followPrimary(ctx)
		}
		switch {
		case err != nil || moved:
		case phase == phasePreparing:
			err = a.prepare(ctx)
		case phase == phaseCopying:
			err = a.copyAll(ctx)
		case phase == phaseShipping:
			if !paused {
				err = a.shipAll(ctx, false)
			}
		case phase == phaseCutover:
			err = a.cutover(ctx)
		default:
			a.stopRunning("")
			return
		}
		if ctx.Err() != nil {
			return
		}
		var fatalErr *fatalError
		if errors.As(err, &fatalErr) {
			a.fail(ctx, fatalErr)
			return
		}
		wait := time.Duration(0)
		if err != nil {
			log.Printf("%s: %v (retrying in %s)", phase, err, delay)
			a.mu.Lock()
			a.transient = "Retrying: " + err.Error()
			a.mu.Unlock()
			wait, delay = delay, min(2*delay, maxRetryDelay)
		} else {
			a.mu.Lock()
			a.transient = ""
			a.mu.Unlock()
			delay = time.Second
			if phase == phaseShipping && !moved {
				wait = interval - time.Since(started)
				if paused {
					wait = time.Hour
				}
			}
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
			case <-time.After(wait):
			}
		}
	}
}

func (a *Agent) stopRunning(failure string) {
	a.mu.Lock()
	a.running = false
	if failure != "" {
		a.failure = failure
	}
	a.mu.Unlock()
}

// fail persists the failure, so a restarted worker keeps reporting it
// instead of repeating work, then stops the run.
func (a *Agent) fail(ctx context.Context, failure *fatalError) {
	log.Printf("migration failed: %s", failure.message)
	if db, err := a.replicaDB(); err == nil {
		writeCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
		if _, err := db.ExecContext(writeCtx, setFailureSQL, truncate(failure.message, 2000), failure.resumable, a.config.Name); err != nil &&
			!isMissingState(err) {
			log.Printf("record the failure on the target: %s", sqlError(err))
		}
		cancel()
	}
	a.mu.Lock()
	a.resumable, a.transient = failure.resumable, ""
	a.mu.Unlock()
	a.stopRunning(failure.message)
}

func truncate(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length-1] + "…"
}

func (a *Agent) setPhase(ctx context.Context, phase string) error {
	db, err := a.replicaDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, setPhaseSQL, phase, a.config.Name); err != nil {
		return fmt.Errorf("record the phase on the target: %s", sqlError(err))
	}
	a.mu.Lock()
	a.phase = phase
	a.mu.Unlock()
	return nil
}

func (a *Agent) saveDatabase(ctx context.Context, state databaseState) error {
	db, err := a.replicaDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, saveDatabaseSQL, state.Stage, state.Seq, state.Stripes, state.FirstLSN, state.LastLSN, nullable(state.TakenAt),
		state.Final, state.Applied, state.AppliedLSN, nullable(state.AppliedAt), a.config.Name, state.Name); err != nil {
		return fmt.Errorf("record the progress of %s on the target: %s", state.Name, sqlError(err))
	}
	a.mu.Lock()
	for i := range a.databases {
		if a.databases[i].Name == state.Name {
			a.databases[i] = state
		}
	}
	a.mu.Unlock()
	return nil
}

func nullable(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// sourceNow reads the source's clock and remembers it for the lag.
func (a *Agent) sourceNow(ctx context.Context) (time.Time, error) {
	source, err := a.source()
	if err != nil {
		return time.Time{}, err
	}
	var now time.Time
	if err := source.QueryRowContext(ctx, `SELECT SYSUTCDATETIME()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("read the source clock: %s", sqlError(err))
	}
	now = time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), time.UTC)
	a.mu.Lock()
	a.sourceClock, a.sourceClockAt = now, time.Now()
	a.mu.Unlock()
	return now, nil
}

// prepare checks the source and target, records the migration on the target
// and sets up the S3 credentials. A failure is persisted and resumable.
func (a *Agent) prepare(ctx context.Context) error {
	a.setStep("Checking the source and target")
	a.mu.Lock()
	options := a.options
	a.mu.Unlock()
	checkCtx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()

	source, err := a.readSource(checkCtx, options)
	if err != nil {
		return fatal(true, "Cannot reach the source: %s", sqlError(err))
	}
	target, err := a.readTarget(checkCtx, a.config.Target, options)
	if err != nil {
		return fatal(true, "Cannot reach the target: %s", sqlError(err))
	}
	// On an availability group, the databases are restored on the primary
	// replica and the migration talks to it directly, so that a failover
	// cannot split the restore chain between replicas.
	replica := a.config.Target
	if host := endpointHost(target.Server.Endpoint); host != "" {
		replica = a.config.Target.WithHost(host)
	}
	a.mu.Lock()
	a.replica, a.sourceServer, a.targetServer = replica, source.Server, target.Server
	a.mu.Unlock()
	if err := a.record(checkCtx, options, source); err != nil {
		return fatal(true, "Cannot record the migration on the target: %s", err)
	}

	problems := checkPreflight(source, target, options)
	problems = append(problems, a.checkStorage(checkCtx)...)
	if len(problems) > 0 {
		return fatal(true, "Cannot start the migration: %s.", strings.Join(problems, "; "))
	}
	if options.RecreateDatabases {
		replicaDB, err := a.replicaDB()
		if err != nil {
			return err
		}
		for _, existing := range target.Existing {
			a.setStep("Dropping " + existing.Name + " on the target")
			statement := fmt.Sprintf("IF DATABASEPROPERTYEX(%s, 'Status') = 'ONLINE' ALTER DATABASE %s SET SINGLE_USER WITH ROLLBACK IMMEDIATE; "+
				"IF DB_ID(%s) IS NOT NULL DROP DATABASE %s", quoteString(existing.Name), quoteName(existing.Name), quoteString(existing.Name), quoteName(existing.Name))
			if _, err := replicaDB.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("drop %s on the target: %s", existing.Name, sqlError(err))
			}
		}
	}
	return a.setPhase(ctx, phaseCopying)
}

func (a *Agent) readSource(ctx context.Context, options Options) (sourceFacts, error) {
	db, err := a.source()
	if err != nil {
		return sourceFacts{}, err
	}
	var facts sourceFacts
	if facts.Server, err = readServerFacts(ctx, db); err != nil {
		return facts, err
	}
	if facts.Databases, err = readDatabaseFacts(ctx, db, options.Databases); err != nil {
		return facts, err
	}
	recent, err := readForeignLogBackups(ctx, db, a.config.Storage.CredentialName()+"/",
		"bs.backup_finish_date > DATEADD(hour, -24, GETDATE())", nil, options.Databases)
	if err != nil {
		return facts, err
	}
	credentials, err := queryStrings(ctx, db, migrationCredentialsSQL)
	facts.Recent = withoutFinishedMigrations(recent, credentials)
	return facts, err
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (a *Agent) readTarget(ctx context.Context, endpoint Endpoint, options Options) (targetFacts, error) {
	db, err := a.db(endpoint)
	if err != nil {
		return targetFacts{}, err
	}
	var facts targetFacts
	if facts.Server, err = readServerFacts(ctx, db); err != nil {
		return facts, err
	}
	if facts.Existing, err = readDatabaseFacts(ctx, db, options.Databases); err != nil {
		return facts, err
	}
	rows, err := db.QueryContext(ctx, `SELECT CONVERT(varchar(42), thumbprint, 1) FROM master.sys.certificates`)
	if err != nil {
		return facts, err
	}
	defer rows.Close()
	for rows.Next() {
		var thumbprint string
		if err := rows.Scan(&thumbprint); err != nil {
			return facts, err
		}
		facts.Certificates = append(facts.Certificates, thumbprint)
	}
	return facts, rows.Err()
}

// record creates the state on the target, once: a resumed preflight finds
// it there.
func (a *Agent) record(ctx context.Context, options Options, source sourceFacts) error {
	db, err := a.replicaDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, createStateTablesSQL); err != nil {
		return errors.New(sqlError(err))
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	a.mu.Lock()
	replica := a.replica.Host
	a.mu.Unlock()
	if _, err := db.ExecContext(ctx, `IF NOT EXISTS (SELECT 1 FROM `+stateTable+` WHERE name = @p1) `+insertStateSQL,
		a.config.Name, replica, string(encoded), phasePreparing); err != nil {
		return errors.New(sqlError(err))
	}
	if _, err := db.ExecContext(ctx, `UPDATE `+stateTable+` SET replica = @p2 WHERE name = @p1 AND phase = 'preparing'`,
		a.config.Name, replica); err != nil {
		return errors.New(sqlError(err))
	}
	for i, name := range options.Databases {
		facts, _ := findDatabase(source.Databases, name)
		if _, err := db.ExecContext(ctx, `IF NOT EXISTS (SELECT 1 FROM `+databaseTable+` WHERE name = @p1 AND database_name = @p2) `+insertDatabaseSQL,
			a.config.Name, name, i, facts.DataBytes); err != nil {
			return errors.New(sqlError(err))
		}
		if _, err := db.ExecContext(ctx, `UPDATE `+databaseTable+` SET size_bytes = @p3 WHERE name = @p1 AND database_name = @p2 AND seq = -1`,
			a.config.Name, name, facts.DataBytes); err != nil {
			return errors.New(sqlError(err))
		}
	}
	databases, err := readDatabases(ctx, db, a.config.Name)
	if err != nil {
		return errors.New(sqlError(err))
	}
	a.mu.Lock()
	a.databases = databases
	a.mu.Unlock()
	return nil
}

// checkStorage writes a probe object, then makes both servers read it with
// the migration's credential: SQL Server must trust the endpoint's
// certificate, which a worker-side check cannot prove.
func (a *Agent) checkStorage(ctx context.Context) []string {
	storage := a.config.Storage
	if err := storage.Put(ctx, probeKey, []byte("openeverest migration probe\n")); err != nil {
		return []string{"the worker cannot write to the backup storage: " + err.Error()}
	}
	source, err := a.source()
	if err != nil {
		return []string{err.Error()}
	}
	replica, err := a.replicaDB()
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, side := range []struct {
		role string
		db   *sql.DB
	}{{"source", source}, {"target", replica}} {
		if err := ensureCredential(ctx, side.db, storage); err != nil {
			problems = append(problems, fmt.Sprintf("cannot create the S3 credential on the %s: %s", side.role, sqlError(err)))
			continue
		}
		_, err := side.db.ExecContext(ctx, headerStatement(storage.URL(probeKey), storage.Region))
		if hasSQLError(err, errCannotOpenMedia) {
			problems = append(problems, fmt.Sprintf("the %s SQL Server cannot read the backup storage (%s); it must trust the storage's "+
				"TLS certificate (for an OpenEverest instance, attach the backup storage to it) and reach the endpoint", side.role, sqlError(err)))
		}
	}
	if err := storage.Remove(ctx, probeKey); err != nil {
		log.Printf("remove the probe object: %v", err)
	}
	return problems
}

func ensureCredential(ctx context.Context, db *sql.DB, storage *Storage) error {
	var exists int
	if err := db.QueryRowContext(ctx, credentialExistsSQL, storage.CredentialName()).Scan(&exists); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, credentialStatement(storage.CredentialName(), storage.AccessKey, storage.SecretKey, exists > 0))
	return err
}

func (a *Agent) databaseStates() []databaseState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]databaseState(nil), a.databases...)
}

// copyAll restores a full backup of every database WITH NORECOVERY.
func (a *Agent) copyAll(ctx context.Context) error {
	if err := a.ensureCredentials(ctx); err != nil {
		return err
	}
	for _, state := range a.databaseStates() {
		if state.Stage != dbCopying {
			continue
		}
		if err := a.copyDatabase(ctx, state); err != nil {
			return err
		}
	}
	a.setStep(shippingStep)
	return a.setPhase(ctx, phaseShipping)
}

func (a *Agent) copyDatabase(ctx context.Context, state databaseState) error {
	storage := a.config.Storage
	sourceServer, targetServer, err := a.servers(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.copying = &copyProgress{database: state.Name, size: state.SizeBytes, backupDone: state.Seq == 0}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.copying = nil
		a.mu.Unlock()
	}()
	source, err := a.source()
	if err != nil {
		return err
	}
	replica, err := a.replicaDB()
	if err != nil {
		return err
	}
	if state.Seq < 0 {
		a.setStep("Backing up " + state.Name + " on the source")
		factsCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
		facts, err := readDatabaseFacts(factsCtx, source, []string{state.Name})
		cancel()
		if err != nil {
			return fmt.Errorf("read %s on the source: %s", state.Name, sqlError(err))
		}
		database, found := findDatabase(facts, state.Name)
		if !found || database.State != "ONLINE" {
			return fmt.Errorf("database %s is not online on the source", state.Name)
		}
		stripes := int(min(max((database.DataBytes+stripeBytes-1)/stripeBytes, 1), maxStripes))
		urls := make([]string, stripes)
		for i := range urls {
			urls[i] = storage.URL(fullBackupKey(state.Name, i+1, stripes))
		}
		takenAt, err := a.sourceNow(ctx)
		if err != nil {
			return err
		}
		// A copy-only backup leaves the source's differential base alone. A
		// database that never had a full backup has no log chain yet; only
		// a regular full backup starts one.
		params := backupParams{Database: state.Name, URLs: urls, CopyOnly: database.HasLogChain, Compress: sourceServer.compress(),
			Region: storage.Region}
		if err := a.runTracked(ctx, source, backupStatement(params), func(session int64) {
			a.mu.Lock()
			a.copying.backupSession = session
			a.mu.Unlock()
		}); err != nil {
			return fmt.Errorf("back up %s on the source: %s", state.Name, sqlError(err))
		}
		first, last, err := a.backupLSNs(ctx, state.Name, urls[0])
		if err != nil {
			return err
		}
		state.Seq, state.Stripes, state.FirstLSN, state.LastLSN, state.TakenAt, state.Applied = 0, stripes, first, last, &takenAt, false
		state.SizeBytes = max(state.SizeBytes, database.DataBytes)
		if err := a.saveDatabase(ctx, state); err != nil {
			return err
		}
		a.mu.Lock()
		a.copying.backupDone = true
		a.mu.Unlock()
	}

	a.setStep("Restoring " + state.Name + " on the target")
	urls := make([]string, state.Stripes)
	for i := range urls {
		urls[i] = storage.URL(fullBackupKey(state.Name, i+1, state.Stripes))
	}
	moves, err := a.fileMoves(ctx, replica, state.Name, urls[0], targetServer)
	if hasSQLError(err, errCannotOpenMedia) {
		// The backup is gone from the bucket; take it again.
		state.Seq = -1
		_ = a.saveDatabase(ctx, state)
		return fmt.Errorf("read the full backup of %s: %s", state.Name, sqlError(err))
	}
	if err != nil {
		return fmt.Errorf("read the full backup of %s: %s", state.Name, sqlError(err))
	}
	// REPLACE overwrites only this migration's own partial restore: the
	// preflight refused (or, when asked to, dropped) existing databases.
	restore := restoreStatement(restoreParams{Database: state.Name, URLs: urls, Replace: true, Moves: moves, Region: storage.Region})
	if err := a.runTracked(ctx, replica, restore, func(session int64) {
		a.mu.Lock()
		a.copying.restoreSession = session
		a.mu.Unlock()
	}); err != nil {
		return fmt.Errorf("restore %s on the target: %s", state.Name, sqlError(err))
	}
	state.Stage, state.Applied, state.AppliedLSN, state.AppliedAt = dbShipping, true, state.LastLSN, state.TakenAt
	return a.saveDatabase(ctx, state)
}

// servers returns the facts the preflight read, reading them again after a
// worker restart.
func (a *Agent) servers(ctx context.Context) (serverFacts, serverFacts, error) {
	a.mu.Lock()
	sourceServer, targetServer := a.sourceServer, a.targetServer
	a.mu.Unlock()
	if sourceServer.Name != "" && targetServer.Name != "" {
		return sourceServer, targetServer, nil
	}
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	source, err := a.source()
	if err != nil {
		return sourceServer, targetServer, err
	}
	if sourceServer, err = readServerFacts(ctx, source); err != nil {
		return sourceServer, targetServer, fmt.Errorf("read the source: %s", sqlError(err))
	}
	replica, err := a.replicaDB()
	if err != nil {
		return sourceServer, targetServer, err
	}
	if targetServer, err = readServerFacts(ctx, replica); err != nil {
		return sourceServer, targetServer, fmt.Errorf("read the target: %s", sqlError(err))
	}
	a.mu.Lock()
	a.sourceServer, a.targetServer = sourceServer, targetServer
	a.mu.Unlock()
	return sourceServer, targetServer, nil
}

// runTracked runs a long statement on its own connection and reports the
// connection's session ID, so the status can read its progress.
func (a *Agent) runTracked(ctx context.Context, db *sql.DB, statement string, session func(int64)) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var id int64
	if err := conn.QueryRowContext(ctx, `SELECT @@SPID`).Scan(&id); err == nil {
		session(id)
	}
	_, err = conn.ExecContext(ctx, statement)
	return err
}

// backupLSNs reads a backup's LSN range from the source's history, or from
// the backup itself when the history is on another replica.
func (a *Agent) backupLSNs(ctx context.Context, database, url string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	source, err := a.source()
	if err != nil {
		return "", "", err
	}
	var first, last string
	err = source.QueryRowContext(ctx, `SELECT TOP 1 CONVERT(varchar(40), bs.first_lsn), CONVERT(varchar(40), bs.last_lsn)
		FROM msdb.dbo.backupset bs JOIN msdb.dbo.backupmediafamily bmf ON bmf.media_set_id = bs.media_set_id
		WHERE bmf.physical_device_name = @p1 AND bs.database_name = @p2 ORDER BY bs.backup_set_id DESC`, url, database).Scan(&first, &last)
	if err == nil {
		return first, last, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("read the backup history of %s on the source: %s", database, sqlError(err))
	}
	header, err := a.readHeader(ctx, url)
	if err != nil {
		return "", "", err
	}
	return header.FirstLSN, header.LastLSN, nil
}

type backupHeader struct {
	FirstLSN, LastLSN, DatabaseName string
	BackupType                      int
}

// readHeader runs RESTORE HEADERONLY on the target. Columns are read by
// name: the result set differs between versions.
func (a *Agent) readHeader(ctx context.Context, url string) (backupHeader, error) {
	replica, err := a.replicaDB()
	if err != nil {
		return backupHeader{}, err
	}
	rows, err := replica.QueryContext(ctx, headerStatement(url, a.config.Storage.Region))
	if err != nil {
		return backupHeader{}, fmt.Errorf("read the backup header: %s", sqlError(err))
	}
	defer rows.Close()
	values, err := firstRow(rows)
	if err != nil {
		return backupHeader{}, fmt.Errorf("read the backup header: %s", sqlError(err))
	}
	header := backupHeader{FirstLSN: values["firstlsn"], LastLSN: values["lastlsn"], DatabaseName: values["databasename"]}
	fmt.Sscan(values["backuptype"], &header.BackupType)
	if header.FirstLSN == "" || header.LastLSN == "" {
		return backupHeader{}, errors.New("the backup header has no LSN range")
	}
	return header, nil
}

// firstRow returns the first row of a result set as strings keyed by
// lowercase column name.
func firstRow(rows *sql.Rows) (map[string]string, error) {
	values, err := allRows(rows)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("no rows")
	}
	return values[0], nil
}

func allRows(rows *sql.Rows) ([]map[string]string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []map[string]string
	for rows.Next() {
		cells := make([]sql.NullString, len(columns))
		pointers := make([]any, len(columns))
		for i := range cells {
			pointers[i] = &cells[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]string, len(columns))
		for i, column := range columns {
			row[strings.ToLower(column)] = cells[i].String
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var unsafeFileCharacters = regexp.MustCompile(`[^A-Za-z0-9_.$-]`)

// fileMoves places every file of the backup under the target's default data
// and log paths: the source's paths need not exist on the target.
func (a *Agent) fileMoves(ctx context.Context, replica *sql.DB, database, url string, target serverFacts) ([]fileMove, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	rows, err := replica.QueryContext(ctx, fileListStatement(url, a.config.Storage.Region))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files, err := allRows(rows)
	if err != nil {
		return nil, err
	}
	return planMoves(database, files, target.DataPath, target.LogPath), nil
}

func planMoves(database string, files []map[string]string, dataPath, logPath string) []fileMove {
	moves := make([]fileMove, 0, len(files))
	used := map[string]bool{}
	for _, file := range files {
		logical := file["logicalname"]
		extension := path.Ext(strings.ReplaceAll(file["physicalname"], `\`, "/"))
		directory := dataPath
		if file["type"] == "L" {
			directory = logPath
			if extension == "" {
				extension = ".ldf"
			}
		} else if extension == "" {
			extension = ".ndf"
		}
		name := database + "_" + unsafeFileCharacters.ReplaceAllString(logical, "_")
		for candidate, n := name, 2; ; n++ {
			if !used[strings.ToLower(candidate+extension)] {
				name = candidate
				break
			}
			candidate = fmt.Sprintf("%s_%d", name, n)
		}
		used[strings.ToLower(name+extension)] = true
		moves = append(moves, fileMove{LogicalName: logical, PhysicalPath: strings.TrimRight(directory, `/\`) + "/" + name + extension})
	}
	return moves
}

// shipAll takes and restores one log backup of every database. In the
// cutover, it takes each database's final backup instead.
func (a *Agent) shipAll(ctx context.Context, final bool) error {
	if err := a.ensureCredentials(ctx); err != nil {
		return err
	}
	for _, state := range a.databaseStates() {
		if state.Stage != dbShipping {
			continue
		}
		if err := a.shipDatabase(ctx, state, final); err != nil {
			return err
		}
	}
	if !final {
		_, err := a.sourceNow(ctx)
		return err
	}
	return nil
}

func (a *Agent) shipDatabase(ctx context.Context, state databaseState, final bool) error {
	storage := a.config.Storage
	if !state.Applied {
		var err error
		if state, err = a.restoreLog(ctx, state); err != nil {
			return err
		}
	}
	if state.Final {
		return nil
	}
	a.mu.Lock()
	options := a.options
	a.mu.Unlock()
	next := state.Seq + 1
	key := logBackupKey(state.Name, next)
	url := storage.URL(key)

	// A backup taken just before a worker restart, but never recorded.
	exists, err := storage.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		header, err := a.readHeader(ctx, url)
		if err != nil {
			return err
		}
		if lsnLess(state.AppliedLSN, header.FirstLSN) || !lsnLess(state.AppliedLSN, header.LastLSN) {
			// Not the next link of this chain: left over from an earlier one.
			log.Printf("%s: removing stale log backup %d", state.Name, next)
			if err := storage.Remove(ctx, key); err != nil {
				return err
			}
			return nil
		}
		log.Printf("%s: adopting log backup %d taken before a restart", state.Name, next)
		// Its start time is unknown: the previous backup's is a safe bound.
		state.Seq, state.FirstLSN, state.LastLSN, state.Final, state.Applied = next, header.FirstLSN, header.LastLSN, false, false
		if err := a.saveDatabase(ctx, state); err != nil {
			return err
		}
		_, err = a.restoreLog(ctx, state)
		return err
	}

	source, err := a.source()
	if err != nil {
		return err
	}
	if err := a.checkForeignLogBackups(ctx, source, state); err != nil {
		return err
	}
	noRecovery := final && options.LeaveSourceRestoring
	if noRecovery {
		restoring, err := sourceRestoring(ctx, source, state.Name)
		if err != nil {
			return err
		}
		if restoring {
			// The tail-log backup was taken, adopted and restored already.
			state.Final = true
			return a.saveDatabase(ctx, state)
		}
	}
	sourceServer, _, err := a.servers(ctx)
	if err != nil {
		return err
	}
	takenAt, err := a.sourceNow(ctx)
	if err != nil {
		return err
	}
	statement := backupStatement(backupParams{Database: state.Name, URLs: []string{url}, Log: true, NoRecovery: noRecovery,
		Compress: sourceServer.compress(), Region: storage.Region})
	if noRecovery {
		// A tail-log backup WITH NORECOVERY needs exclusive access.
		statement = fmt.Sprintf("ALTER DATABASE %s SET SINGLE_USER WITH ROLLBACK IMMEDIATE; %s", quoteName(state.Name), statement)
	}
	if _, err := source.ExecContext(ctx, statement); err != nil {
		if noRecovery {
			if _, resetErr := source.ExecContext(context.WithoutCancel(ctx), "ALTER DATABASE "+quoteName(state.Name)+" SET MULTI_USER"); resetErr != nil {
				log.Printf("%s: let users in again on the source: %s", state.Name, sqlError(resetErr))
			}
		}
		return fmt.Errorf("back up the log of %s on the source: %s", state.Name, sqlError(err))
	}
	first, last, err := a.backupLSNs(ctx, state.Name, url)
	if err != nil {
		return err
	}
	if lsnLess(state.AppliedLSN, first) {
		return a.brokenChain(ctx, source, state)
	}
	state.Seq, state.FirstLSN, state.LastLSN, state.TakenAt, state.Final, state.Applied = next, first, last, &takenAt, final, false
	if err := a.saveDatabase(ctx, state); err != nil {
		return err
	}
	_, err = a.restoreLog(ctx, state)
	return err
}

func sourceRestoring(ctx context.Context, source *sql.DB, database string) (bool, error) {
	var state sql.NullString
	if err := source.QueryRowContext(ctx, `SELECT CAST(DATABASEPROPERTYEX(@p1, 'Status') AS nvarchar(60))`, database).Scan(&state); err != nil {
		return false, fmt.Errorf("read %s on the source: %s", database, sqlError(err))
	}
	return state.String == "RESTORING", nil
}

// checkForeignLogBackups fails the migration when a log backup that is not
// the migration's took log records the target has not restored yet.
func (a *Agent) checkForeignLogBackups(ctx context.Context, source *sql.DB, state databaseState) error {
	if state.AppliedLSN == "" {
		return nil
	}
	foreign, err := readForeignLogBackups(ctx, source, a.config.Storage.CredentialName()+"/",
		"bs.last_lsn > CAST(@p2 AS numeric(25,0))", []any{state.AppliedLSN}, []string{state.Name})
	if err != nil {
		return fmt.Errorf("read the backup history of %s on the source: %s", state.Name, sqlError(err))
	}
	if len(foreign) > 0 {
		return fatal(false, "A log backup of %s was taken outside the migration; its log chain is broken. "+
			"Disable other log backups on the source (for an OpenEverest instance: point-in-time recovery), then delete this migration "+
			"and create it again.", foreign[0])
	}
	return nil
}

func (a *Agent) brokenChain(ctx context.Context, source *sql.DB, state databaseState) error {
	foreign, _ := readForeignLogBackups(ctx, source, a.config.Storage.CredentialName()+"/",
		"bs.last_lsn > CAST(@p2 AS numeric(25,0))", []any{state.AppliedLSN}, []string{state.Name})
	cause := "another log backup"
	if len(foreign) > 0 {
		cause = "a log backup of " + foreign[0].String()
	}
	return fatal(false, "The log chain of %s is broken: %s was taken outside the migration. Disable other log backups on the source "+
		"(for an OpenEverest instance: point-in-time recovery), then delete this migration and create it again.", state.Name, cause)
}

// restoreLog restores the database's last recorded log backup.
func (a *Agent) restoreLog(ctx context.Context, state databaseState) (databaseState, error) {
	storage := a.config.Storage
	replica, err := a.replicaDB()
	if err != nil {
		return state, err
	}
	key := logBackupKey(state.Name, state.Seq)
	_, err = replica.ExecContext(ctx, restoreStatement(restoreParams{Database: state.Name, URLs: []string{storage.URL(key)}, Log: true, Region: storage.Region}))
	switch {
	case hasSQLError(err, errLogTooEarly):
		log.Printf("%s: log backup %d was restored already", state.Name, state.Seq)
	case hasSQLError(err, errLogTooRecent):
		return state, fatal(false, "The log chain of %s is broken: the target misses log records before log backup %d. "+
			"Delete this migration and create it again.", state.Name, state.Seq)
	case hasSQLError(err, errNotRestoring):
		return state, fatal(false, "Database %s on the target is no longer restoring; it was recovered outside the migration. "+
			"Delete this migration and create it again with \"Recreate the target databases\".", state.Name)
	case err != nil:
		return state, fmt.Errorf("restore the log of %s on the target: %s", state.Name, sqlError(err))
	}
	state.Applied, state.AppliedLSN, state.AppliedAt = true, state.LastLSN, state.TakenAt
	if err := a.saveDatabase(ctx, state); err != nil {
		return state, err
	}
	stale := []string{key}
	if state.Seq == 1 {
		// The full backup is useless once a log backup was restored on it.
		for stripe := 1; stripe <= state.Stripes; stripe++ {
			stale = append(stale, fullBackupKey(state.Name, stripe, state.Stripes))
		}
	}
	if err := storage.Remove(ctx, stale...); err != nil {
		log.Printf("%s: remove restored backups: %v", state.Name, err)
	}
	return state, nil
}

// cutover takes the final log backups, restores them, recovers the
// databases, copies the logins and cleans up.
func (a *Agent) cutover(ctx context.Context) error {
	for _, state := range a.databaseStates() {
		if state.Stage == dbCopying {
			return fatal(true, "Database %s is still being copied; resume the migration and request the cutover once it caught up.", state.Name)
		}
	}
	a.setStep("Taking and restoring the final log backups")
	for !allFinal(a.databaseStates()) {
		if err := a.shipAll(ctx, true); err != nil {
			return err
		}
	}
	replica, err := a.replicaDB()
	if err != nil {
		return err
	}
	server, err := readServerFacts(ctx, replica)
	if err != nil {
		return fmt.Errorf("read the target: %s", sqlError(err))
	}
	if server.Hadr && server.Role != "" && server.Role != "PRIMARY" {
		return fatal(true, "The databases were restored on %s, which is no longer the availability group's primary. "+
			"Fail the availability group over to it, then resume the cutover.", server.Name)
	}
	a.mu.Lock()
	options := a.options
	a.mu.Unlock()
	for _, state := range a.databaseStates() {
		if state.Stage == dbRecovered {
			continue
		}
		a.setStep("Recovering " + state.Name + " on the target")
		if _, err := replica.ExecContext(ctx, fmt.Sprintf("IF DATABASEPROPERTYEX(%s, 'Status') = 'RESTORING' RESTORE DATABASE %s WITH RECOVERY",
			quoteString(state.Name), quoteName(state.Name))); err != nil {
			return fmt.Errorf("recover %s on the target: %s", state.Name, sqlError(err))
		}
		// The tail-log backup carried the source's SINGLE_USER setting.
		if _, err := replica.ExecContext(ctx, fmt.Sprintf("IF DATABASEPROPERTYEX(%s, 'UserAccess') <> 'MULTI_USER' ALTER DATABASE %s SET MULTI_USER",
			quoteString(state.Name), quoteName(state.Name))); err != nil {
			return fmt.Errorf("open %s to users on the target: %s", state.Name, sqlError(err))
		}
		state.Stage = dbRecovered
		if err := a.saveDatabase(ctx, state); err != nil {
			return err
		}
	}

	var notes []string
	if options.CopyLogins {
		a.setStep("Copying logins")
		loginNotes, err := a.copyLogins(ctx, options.Databases)
		if err != nil {
			return err
		}
		notes = append(notes, loginNotes...)
	}
	if server.Hadr && server.Role == "PRIMARY" {
		notes = append(notes, "The operator adds the databases to the availability group and seeds the secondary replicas.")
	}
	if options.LeaveSourceRestoring {
		notes = append(notes, "The source databases are left restoring.")
	}
	a.setStep("Cleaning up")
	notes = append(notes, a.cleanup(ctx)...)
	note := strings.Join(notes, " ")
	if _, err := replica.ExecContext(ctx, setCompletedSQL, note, a.config.Name); err != nil {
		return fmt.Errorf("record the completion on the target: %s", sqlError(err))
	}
	a.mu.Lock()
	a.stage, a.phase, a.notes, a.step = stageCompleted, phaseCompleted, note, ""
	a.mu.Unlock()
	log.Print("migration completed")
	return nil
}

// allFinal tells whether every database restored its final log backup.
func allFinal(databases []databaseState) bool {
	for _, state := range databases {
		if state.Stage == dbShipping && !(state.Final && state.Applied) {
			return false
		}
	}
	return true
}

// cleanup removes the credentials and the bucket's objects; the cutover
// succeeded already, so problems become notes.
func (a *Agent) cleanup(ctx context.Context) []string {
	var notes []string
	credential := a.config.Storage.CredentialName()
	a.mu.Lock()
	pinned := a.replica
	a.mu.Unlock()
	for _, side := range []struct {
		role     string
		endpoint Endpoint
	}{{"source", a.config.Source}, {"target", pinned}} {
		if err := a.dropCredential(ctx, side.endpoint); err != nil {
			log.Printf("drop the S3 credential on the %s: %v", side.role, err)
			notes = append(notes, fmt.Sprintf("Drop the credential %s on the %s (%v).", credential, side.role, err))
		}
	}
	if err := a.config.Storage.RemoveAll(ctx); err != nil {
		log.Printf("remove the backups: %v", err)
		notes = append(notes, fmt.Sprintf("Remove the objects under %s/ from the bucket.", a.config.Storage.Prefix))
	}
	return slices.Clip(notes)
}
