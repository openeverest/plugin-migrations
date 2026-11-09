package main

import (
	"errors"
	"fmt"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"
)

// T-SQL builders, adapted from the mssql-operator's mssqlclient package.

// quoteName renders a delimited identifier; a closing bracket is doubled.
func quoteName(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// quoteString renders a Unicode string literal.
func quoteString(value string) string {
	return "N'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func urlList(urls []string) string {
	parts := make([]string, len(urls))
	for i, u := range urls {
		parts[i] = "URL = " + quoteString(u)
	}
	return strings.Join(parts, ", ")
}

// s3Options renders the region option of BACKUP and RESTORE; SQL Server
// defaults to us-east-1. Regions are validated at startup.
func s3Options(key, region string) string {
	return fmt.Sprintf(`%s = '{"s3": {"region":"%s"}}'`, key, region)
}

type backupParams struct {
	Database   string
	URLs       []string
	Log        bool
	CopyOnly   bool
	NoRecovery bool
	// Compress is false on editions without backup compression (Express).
	Compress bool
	Region   string
}

func backupStatement(p backupParams) string {
	verb := "BACKUP DATABASE"
	if p.Log {
		verb = "BACKUP LOG"
	}
	options := []string{"FORMAT"}
	if p.Compress {
		options = append(options, "COMPRESSION")
	}
	if p.CopyOnly {
		options = append(options, "COPY_ONLY")
	}
	if p.NoRecovery {
		options = append(options, "NORECOVERY")
	}
	if p.Region != "" {
		options = append(options, s3Options("BACKUP_OPTIONS", p.Region))
	}
	return fmt.Sprintf("%s %s TO %s WITH %s", verb, quoteName(p.Database), urlList(p.URLs), strings.Join(options, ", "))
}

type fileMove struct {
	LogicalName, PhysicalPath string
}

type restoreParams struct {
	Database string
	URLs     []string
	Log      bool
	Replace  bool
	Moves    []fileMove
	Region   string
}

// restoreStatement always restores WITH NORECOVERY: the cutover recovers
// the database separately, once the last log is restored.
func restoreStatement(p restoreParams) string {
	verb := "RESTORE DATABASE"
	if p.Log {
		verb = "RESTORE LOG"
	}
	options := make([]string, 0, len(p.Moves)+3)
	for _, move := range p.Moves {
		options = append(options, fmt.Sprintf("MOVE %s TO %s", quoteString(move.LogicalName), quoteString(move.PhysicalPath)))
	}
	if p.Replace {
		options = append(options, "REPLACE")
	}
	options = append(options, "NORECOVERY")
	if p.Region != "" {
		options = append(options, s3Options("RESTORE_OPTIONS", p.Region))
	}
	return fmt.Sprintf("%s %s FROM %s WITH %s", verb, quoteName(p.Database), urlList(p.URLs), strings.Join(options, ", "))
}

func headerStatement(url, region string) string {
	statement := "RESTORE HEADERONLY FROM URL = " + quoteString(url)
	if region != "" {
		statement += " WITH " + s3Options("RESTORE_OPTIONS", region)
	}
	return statement
}

func fileListStatement(url, region string) string {
	statement := "RESTORE FILELISTONLY FROM URL = " + quoteString(url)
	if region != "" {
		statement += " WITH " + s3Options("RESTORE_OPTIONS", region)
	}
	return statement
}

// credentialStatement creates or updates the S3 credential. DDL takes no
// parameters, so the secret is inlined; never log the statement.
func credentialStatement(name, accessKey, secretKey string, exists bool) string {
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	return fmt.Sprintf("%s CREDENTIAL %s WITH IDENTITY = 'S3 Access Key', SECRET = %s",
		verb, quoteName(name), quoteString(accessKey+":"+secretKey))
}

const credentialExistsSQL = `SELECT COUNT(*) FROM sys.credentials WHERE name = @p1`

func dropCredentialStatement(name string) string {
	return fmt.Sprintf("IF EXISTS (SELECT 1 FROM sys.credentials WHERE name = %s) DROP CREDENTIAL %s", quoteString(name), quoteName(name))
}

// The migration state lives in the target's master database, on the replica
// the databases are restored on. A user database would be adopted into an
// availability group by the operator, and master is never restored over.
const (
	stateTable    = "master.dbo.openeverest_migration"
	databaseTable = "master.dbo.openeverest_migration_database"
)

const createStateTablesSQL = `
IF OBJECT_ID(N'` + stateTable + `', N'U') IS NULL
CREATE TABLE ` + stateTable + ` (
	name nvarchar(64) NOT NULL PRIMARY KEY,
	replica nvarchar(300) NOT NULL,
	options nvarchar(max) NOT NULL,
	phase varchar(16) NOT NULL,
	paused bit NOT NULL DEFAULT 0,
	failure nvarchar(2000) NOT NULL DEFAULT N'',
	resumable bit NOT NULL DEFAULT 1,
	notes nvarchar(max) NOT NULL DEFAULT N'',
	updated_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME()
);
IF OBJECT_ID(N'` + databaseTable + `', N'U') IS NULL
CREATE TABLE ` + databaseTable + ` (
	name nvarchar(64) NOT NULL,
	database_name nvarchar(128) NOT NULL,
	position int NOT NULL,
	stage varchar(16) NOT NULL,
	size_bytes bigint NOT NULL DEFAULT 0,
	seq int NOT NULL DEFAULT -1,
	stripes int NOT NULL DEFAULT 1,
	first_lsn varchar(40) NOT NULL DEFAULT '',
	last_lsn varchar(40) NOT NULL DEFAULT '',
	taken_at datetime2 NULL,
	final bit NOT NULL DEFAULT 0,
	applied bit NOT NULL DEFAULT 0,
	applied_lsn varchar(40) NOT NULL DEFAULT '',
	applied_at datetime2 NULL,
	PRIMARY KEY (name, database_name)
);`

const (
	stateTableExistsSQL = `SELECT CASE WHEN OBJECT_ID(N'` + stateTable + `', N'U') IS NULL THEN 0 ELSE 1 END`
	readStateSQL        = `SELECT replica, options, phase, paused, failure, resumable, notes, updated_at FROM ` + stateTable + ` WHERE name = @p1`
	insertStateSQL      = `INSERT INTO ` + stateTable + ` (name, replica, options, phase) VALUES (@p1, @p2, @p3, @p4)`
	setPhaseSQL         = `UPDATE ` + stateTable + ` SET phase = @p1, updated_at = SYSUTCDATETIME() WHERE name = @p2`
	setPausedSQL        = `UPDATE ` + stateTable + ` SET paused = @p1, updated_at = SYSUTCDATETIME() WHERE name = @p2`
	setFailureSQL       = `UPDATE ` + stateTable + ` SET failure = @p1, resumable = @p2, updated_at = SYSUTCDATETIME() WHERE name = @p3`
	setCompletedSQL     = `UPDATE ` + stateTable + ` SET phase = 'completed', paused = 0, notes = @p1, updated_at = SYSUTCDATETIME() WHERE name = @p2`
	readDatabasesSQL    = `SELECT database_name, stage, size_bytes, seq, stripes, first_lsn, last_lsn, taken_at, final, applied, applied_lsn, applied_at
		FROM ` + databaseTable + ` WHERE name = @p1 ORDER BY position`
	insertDatabaseSQL = `INSERT INTO ` + databaseTable + ` (name, database_name, position, stage, size_bytes) VALUES (@p1, @p2, @p3, 'copying', @p4)`
	saveDatabaseSQL   = `UPDATE ` + databaseTable + ` SET stage = @p1, seq = @p2, stripes = @p3, first_lsn = @p4, last_lsn = @p5, taken_at = @p6,
		final = @p7, applied = @p8, applied_lsn = @p9, applied_at = @p10 WHERE name = @p11 AND database_name = @p12`
	deleteStateSQL = `IF OBJECT_ID(N'` + databaseTable + `', N'U') IS NOT NULL DELETE FROM ` + databaseTable + ` WHERE name = @p1;
		IF OBJECT_ID(N'` + stateTable + `', N'U') IS NOT NULL DELETE FROM ` + stateTable + ` WHERE name = @p1;`
	// dropEmptyStateSQL removes the tables once no migration uses them.
	dropEmptyStateSQL = `IF OBJECT_ID(N'` + stateTable + `', N'U') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + stateTable + `)
		DROP TABLE ` + stateTable + `;
		IF OBJECT_ID(N'` + databaseTable + `', N'U') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + databaseTable + `)
		DROP TABLE ` + databaseTable + `;`
)

// SQL Server error numbers the worker acts on.
const (
	errLogTooEarly     = 4326 // the log backup was already restored
	errLogTooRecent    = 4305 // a log backup is missing in between
	errNotRestoring    = 3117 // the database is not restoring (was recovered)
	errBadMedia        = 3241 // the object is not a backup
	errCannotOpenMedia = 3201 // the backup device (URL) cannot be opened
	errDatabaseMissing = 3701
)

// sqlNumbers lists every error number of a go-mssqldb error: SQL Server
// reports the cause of a BACKUP or RESTORE failure before the generic 3013.
func sqlNumbers(err error) []int32 {
	var sqlErr mssql.Error
	if !errors.As(err, &sqlErr) {
		return nil
	}
	if len(sqlErr.All) == 0 {
		return []int32{sqlErr.Number}
	}
	numbers := make([]int32, 0, len(sqlErr.All))
	for _, message := range sqlErr.All {
		numbers = append(numbers, message.Number)
	}
	return numbers
}

func hasSQLError(err error, number int32) bool {
	for _, n := range sqlNumbers(err) {
		if n == number {
			return true
		}
	}
	return false
}

// sqlError joins every message of a SQL Server error; the driver keeps only
// the last one, usually the generic "terminating abnormally".
func sqlError(err error) string {
	var sqlErr mssql.Error
	if errors.As(err, &sqlErr) && len(sqlErr.All) > 1 {
		parts := make([]string, 0, len(sqlErr.All))
		for _, message := range sqlErr.All {
			if message.Number == 3013 {
				continue
			}
			parts = append(parts, strings.TrimSpace(message.Message))
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if errors.As(err, &sqlErr) {
		return strings.TrimSpace(sqlErr.Message)
	}
	return err.Error()
}

// lsnLess compares two LSNs, written as decimal numbers without leading zeros.
func lsnLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
