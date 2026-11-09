package main

import (
	"context"
	"database/sql"
	"fmt"
)

// flavor is what differs between MySQL and MariaDB: how the data is copied,
// how a replication channel is configured and observed, and what preflight
// requires. The migration lifecycle around it is shared.
type flavor interface {
	// checkSource and checkTarget return preflight problems.
	checkSource(source sourceFacts) []string
	checkTarget(source sourceFacts, target targetFacts) []string
	canReplicate(ctx context.Context, db *sql.DB) bool

	// usesLocalInfile tells whether the copy loads with LOAD DATA LOCAL INFILE.
	usesLocalInfile() bool
	// copy copies the databases and returns the source position the copy is
	// consistent with.
	copy(ctx context.Context, workDir string, source, target Endpoint, options Options,
		exclusions map[string][]string, progress func(loaded uint64)) (string, error)
	definerPolicy(ctx context.Context, db *sql.DB) (definerPolicy, error)

	// configure records position as applied and creates the channel.
	configure(ctx context.Context, db *sql.DB, name string, source Endpoint, position string) error
	readChannel(ctx context.Context, db *sql.DB, name string) (channelState, error)
	readWorkerError(ctx context.Context, db *sql.DB, name string, channel channelState) (workerError, bool)
	setFilters(ctx context.Context, db *sql.DB, name string, options Options) error
	// channel runs START, STOP or RESET ALL; a missing channel is reported
	// as errChannelMissing.
	channel(ctx context.Context, db *sql.DB, name string, action channelAction) error
	// skip makes the applier treat the failed transaction as done.
	skip(ctx context.Context, db *sql.DB, name string, failure workerError) error
	sourcePosition(ctx context.Context, db *sql.DB) (string, error)
	// waitForPosition reports whether the target applied position within seconds.
	waitForPosition(ctx context.Context, db *sql.DB, position string, seconds int) (bool, error)
	// disableOnReplica is the ALTER EVENT clause that disables an event on a replica.
	disableOnReplica() string
}

type channelAction int

const (
	channelStart channelAction = iota
	channelStop
	channelReset
)

var errChannelMissing = fmt.Errorf("the replication channel does not exist")

// startChannel stops the channel, sets its filters and starts it again.
// Neither MySQL nor MariaDB persists channel filters across restarts, so they
// are set again before every start.
func startChannel(ctx context.Context, f flavor, db *sql.DB, name string, options Options) error {
	if err := f.channel(ctx, db, name, channelStop); err != nil {
		return fmt.Errorf("stop replication: %s", serverError(err))
	}
	if err := f.setFilters(ctx, db, name, options); err != nil {
		return fmt.Errorf("set the replication filters: %s", serverError(err))
	}
	if err := f.channel(ctx, db, name, channelStart); err != nil {
		return fmt.Errorf("start replication: %s", serverError(err))
	}
	return nil
}

// removeChannel stops and deletes the channel; a missing channel is fine.
func removeChannel(ctx context.Context, f flavor, db *sql.DB, name string) error {
	for _, action := range []channelAction{channelStop, channelReset} {
		if err := f.channel(ctx, db, name, action); err != nil && err != errChannelMissing {
			return fmt.Errorf("remove the channel: %s", serverError(err))
		}
	}
	return nil
}
