package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

// followPrimary moves the migration to the target availability group's new
// primary when the replica the databases are restored on became a
// secondary: the operator adds databases to the group only on the primary,
// so they could never be recovered there. The log backups restored so far
// are gone from the bucket, so the databases are copied again to the new
// primary, and the restoring copies on the old replica, which are the
// migration's own, are dropped. It reports whether the migration moved.
func (a *Agent) followPrimary(ctx context.Context) (bool, error) {
	a.mu.Lock()
	pinned, options, paused := a.replica, a.options, a.paused
	databases := append([]databaseState(nil), a.databases...)
	a.mu.Unlock()
	if strings.EqualFold(pinned.Host, a.config.Target.Host) {
		return false, nil // not an availability group
	}
	checkCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	pinnedDB, err := a.db(pinned)
	if err != nil {
		return false, err
	}
	facts, err := readServerFacts(checkCtx, pinnedDB)
	if err != nil || !facts.Hadr || facts.Role != "SECONDARY" {
		return false, nil // an unreachable replica is reported by the next step
	}
	targetDB, err := a.db(a.config.Target)
	if err != nil {
		return false, err
	}
	primary, err := readServerFacts(checkCtx, targetDB)
	if err != nil {
		return false, fmt.Errorf("read the target availability group's primary: %s", sqlError(err))
	}
	host := endpointHost(primary.Endpoint)
	if primary.Role != "PRIMARY" || host == "" || strings.EqualFold(host, pinned.Host) {
		return false, errors.New("the target availability group has no primary yet")
	}
	for _, database := range databases {
		if database.Final {
			return false, fatal(true, "The target availability group's primary moved from %s to %s after the final log backup of %s was taken. "+
				"Fail the availability group over to %s, then resume the cutover.", pinned.Host, host, database.Name, pinned.Host)
		}
	}
	next := a.config.Target.WithHost(host)
	nextDB, err := a.db(next)
	if err != nil {
		return false, err
	}
	existing, err := readDatabaseFacts(checkCtx, nextDB, options.Databases)
	if err != nil {
		return false, fmt.Errorf("read the target's new primary: %s", sqlError(err))
	}
	for _, database := range existing {
		// A restoring copy outside the group is the migration's own, left by
		// an earlier move.
		if database.InAG || database.State != "RESTORING" {
			return false, fatal(true, "The target availability group's primary moved to %s, which already has a database %s. "+
				"Drop it there, then resume.", host, database.Name)
		}
	}
	log.Printf("the target's primary moved from %s to %s; copying the databases there again", pinned.Host, host)

	// Record the migration on the new primary first: a restarted worker
	// recovers the newest state it finds on the group's replicas.
	tx, err := nextDB.BeginTx(checkCtx, nil)
	if err != nil {
		return false, fmt.Errorf("record the migration on the target's new primary: %s", sqlError(err))
	}
	if err := a.recordOn(checkCtx, tx, next.Host, options, paused, databases); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("record the migration on the target's new primary: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("record the migration on the target's new primary: %s", sqlError(err))
	}
	// Backups of the old chain must not be adopted by the new one.
	if err := a.config.Storage.RemoveAll(ctx); err != nil {
		log.Printf("remove the old backups: %v", err)
	}
	fresh, err := readDatabases(checkCtx, nextDB, a.config.Name)
	if err != nil {
		return false, fmt.Errorf("read the migration state on the target's new primary: %s", sqlError(err))
	}
	a.mu.Lock()
	a.replica, a.phase, a.databases, a.initialSync = next, phaseCopying, fresh, false
	a.step = fmt.Sprintf("The target's primary moved to %s; copying the databases there again", host)
	a.mu.Unlock()

	for _, name := range options.Databases {
		statement := fmt.Sprintf("IF DATABASEPROPERTYEX(%s, 'Status') = 'RESTORING' DROP DATABASE %s", quoteString(name), quoteName(name))
		if _, err := pinnedDB.ExecContext(checkCtx, statement); err != nil {
			log.Printf("drop the restoring copy of %s on %s: %s", name, pinned.Host, sqlError(err))
		}
	}
	if _, err := pinnedDB.ExecContext(checkCtx, deleteStateSQL, a.config.Name); err != nil {
		log.Printf("remove the migration state on %s: %s", pinned.Host, sqlError(err))
	}
	return true, nil
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// recordOn writes a fresh copying state for the migration on a server,
// replacing one an earlier move may have left there.
func (a *Agent) recordOn(ctx context.Context, db sqlExecer, replica string, options Options, paused bool, databases []databaseState) error {
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{createStateTablesSQL, nil},
		{deleteStateSQL, []any{a.config.Name}},
		{insertStateSQL, []any{a.config.Name, replica, string(encoded), phaseCopying}},
		{setPausedSQL, []any{paused, a.config.Name}},
	}
	for i, database := range databases {
		statements = append(statements, struct {
			query string
			args  []any
		}{insertDatabaseSQL, []any{a.config.Name, database.Name, i, database.SizeBytes}})
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return errors.New(sqlError(err))
		}
	}
	return nil
}
