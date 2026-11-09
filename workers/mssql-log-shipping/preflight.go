package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// maxListedBackups bounds the backups a preflight message lists.
const maxListedBackups = 3

// minMajorVersion is SQL Server 2022, the first release that backs up to
// and restores from S3.
const minMajorVersion = 16

type serverFacts struct {
	Major         int
	Version       string
	Edition       string
	EngineEdition int
	Name          string
	Sysadmin      bool
	StartedAt     string
	Hadr          bool
	DataPath      string
	LogPath       string
	// Role is the local availability replica's role: PRIMARY, SECONDARY or
	// "" outside an availability group.
	Role string
	// Endpoint is the local replica's database mirroring endpoint URL.
	Endpoint string
}

// compress tells whether the edition has backup compression (not Express).
func (f serverFacts) compress() bool { return f.EngineEdition != 4 }

const serverFactsSQL = `SELECT
	ISNULL(CAST(SERVERPROPERTY('ProductMajorVersion') AS int), 0),
	CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)),
	CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
	CAST(SERVERPROPERTY('EngineEdition') AS int),
	CAST(@@SERVERNAME AS nvarchar(256)),
	ISNULL(IS_SRVROLEMEMBER('sysadmin'), 0),
	(SELECT CONVERT(varchar(33), sqlserver_start_time, 126) FROM sys.dm_os_sys_info),
	ISNULL(CAST(SERVERPROPERTY('IsHadrEnabled') AS int), 0),
	ISNULL(CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(512)), N''),
	ISNULL(CAST(SERVERPROPERTY('InstanceDefaultLogPath') AS nvarchar(512)), N''),
	ISNULL((SELECT TOP 1 rs.role_desc FROM sys.dm_hadr_availability_replica_states rs WHERE rs.is_local = 1), N''),
	ISNULL((SELECT TOP 1 ar.endpoint_url FROM sys.availability_replicas ar WHERE ar.replica_server_name = @@SERVERNAME), N'')`

func readServerFacts(ctx context.Context, db *sql.DB) (serverFacts, error) {
	var facts serverFacts
	var sysadmin, hadr int
	err := db.QueryRowContext(ctx, serverFactsSQL).Scan(&facts.Major, &facts.Version, &facts.Edition, &facts.EngineEdition, &facts.Name,
		&sysadmin, &facts.StartedAt, &hadr, &facts.DataPath, &facts.LogPath, &facts.Role, &facts.Endpoint)
	facts.Sysadmin, facts.Hadr = sysadmin == 1, hadr == 1
	return facts, err
}

// endpointHost is the host of a mirroring endpoint URL such as
// TCP://db-0.db.ns.svc.cluster.local:5022.
func endpointHost(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

type databaseFacts struct {
	Name          string
	State         string
	RecoveryModel string
	UserAccess    string
	Encrypted     bool
	InAG          bool
	// HasLogChain is false while a FULL database never had a full backup
	// ("pseudo-simple"): log backups need one first.
	HasLogChain bool
	DataBytes   int64
	// Thumbprint identifies the certificate of an encrypted database's key.
	Thumbprint string
}

// inList appends "(@p<first>, ...)" for values to query and returns args.
func inList(query string, first int, values []string) (string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for i, value := range values {
		placeholders[i] = fmt.Sprintf("@p%d", first+i)
		args[i] = value
	}
	return query + " (" + strings.Join(placeholders, ", ") + ")", args
}

const databaseFactsSQL = `SELECT d.name, d.state_desc, d.recovery_model_desc, d.user_access_desc, d.is_encrypted,
	CASE WHEN d.replica_id IS NULL THEN 0 ELSE 1 END,
	CASE WHEN rs.last_log_backup_lsn IS NULL THEN 0 ELSE 1 END,
	ISNULL((SELECT SUM(CAST(mf.size AS bigint)) * 8192 FROM sys.master_files mf WHERE mf.database_id = d.database_id AND mf.type <> 1), 0),
	ISNULL((SELECT CONVERT(varchar(42), k.encryptor_thumbprint, 1) FROM sys.dm_database_encryption_keys k WHERE k.database_id = d.database_id), '')
FROM sys.databases d LEFT JOIN sys.database_recovery_status rs ON rs.database_id = d.database_id
WHERE d.name IN`

func readDatabaseFacts(ctx context.Context, db *sql.DB, names []string) ([]databaseFacts, error) {
	query, args := inList(databaseFactsSQL, 1, names)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []databaseFacts
	for rows.Next() {
		var facts databaseFacts
		var inAG, chain int
		if err := rows.Scan(&facts.Name, &facts.State, &facts.RecoveryModel, &facts.UserAccess, &facts.Encrypted, &inAG, &chain,
			&facts.DataBytes, &facts.Thumbprint); err != nil {
			return nil, err
		}
		facts.InAG, facts.HasLogChain = inAG == 1, chain == 1
		result = append(result, facts)
	}
	return result, rows.Err()
}

func findDatabase(facts []databaseFacts, name string) (databaseFacts, bool) {
	index := slices.IndexFunc(facts, func(f databaseFacts) bool { return strings.EqualFold(f.Name, name) })
	if index < 0 {
		return databaseFacts{}, false
	}
	return facts[index], true
}

// foreignLogBackupsSQL lists log backups that are not this migration's and
// that would take log records out of its chain. Copy-only log backups
// truncate nothing and are harmless.
const foreignLogBackupsSQL = `SELECT TOP 50 bs.database_name, bmf.physical_device_name, CONVERT(varchar(19), bs.backup_finish_date, 120)
FROM msdb.dbo.backupset bs JOIN msdb.dbo.backupmediafamily bmf ON bmf.media_set_id = bs.media_set_id
WHERE bs.type = 'L' AND bs.is_copy_only = 0 AND LEFT(bmf.physical_device_name, LEN(@p1)) <> @p1 AND %s AND bs.database_name IN`

type foreignBackup struct {
	Database, Device, FinishedAt string
}

func (b foreignBackup) String() string {
	return fmt.Sprintf("%s to %s at %s", b.Database, b.Device, b.FinishedAt)
}

// migrationCredential is the credential name of the OpenEverest migration a
// backup URL belongs to, or "" for other backups.
func migrationCredential(device string) string {
	marker := "/" + keyRoot + "/"
	index := strings.Index(device, marker)
	if !strings.HasPrefix(device, "s3://") || index < 0 {
		return ""
	}
	rest := device[index+len(marker):]
	name, _, found := strings.Cut(rest, "/")
	if !found || name == "" {
		return ""
	}
	return device[:index+len(marker)] + name
}

// withoutFinishedMigrations drops the backups of OpenEverest migrations that
// no longer run: a migration's credential exists on the source until it
// completes or is deleted. A running one ships the same log chain, so its
// backups conflict like any other.
func withoutFinishedMigrations(backups []foreignBackup, credentials []string) []foreignBackup {
	result := make([]foreignBackup, 0, len(backups))
	for _, backup := range backups {
		credential := migrationCredential(backup.Device)
		if credential != "" && !slices.Contains(credentials, credential) {
			continue
		}
		result = append(result, backup)
	}
	return result
}

const migrationCredentialsSQL = `SELECT name FROM sys.credentials WHERE name LIKE N's3://%/` + keyRoot + `/%'`

func readForeignLogBackups(ctx context.Context, db *sql.DB, ourPrefix, condition string, conditionArgs []any, databases []string) ([]foreignBackup, error) {
	query, args := inList(fmt.Sprintf(foreignLogBackupsSQL, condition), 2+len(conditionArgs), databases)
	query += " ORDER BY bs.backup_finish_date DESC"
	rows, err := db.QueryContext(ctx, query, append(append([]any{ourPrefix}, conditionArgs...), args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []foreignBackup
	for rows.Next() {
		var backup foreignBackup
		if err := rows.Scan(&backup.Database, &backup.Device, &backup.FinishedAt); err != nil {
			return nil, err
		}
		result = append(result, backup)
	}
	return result, rows.Err()
}

type sourceFacts struct {
	Server    serverFacts
	Databases []databaseFacts
	// Recent lists log backups taken outside the migration in the last day.
	Recent []foreignBackup
}

type targetFacts struct {
	Server   serverFacts
	Existing []databaseFacts
	// Certificates are the thumbprints of the certificates in master.
	Certificates []string
}

// checkPreflight lists every problem at once so they can be fixed together.
func checkPreflight(source sourceFacts, target targetFacts, options Options) []string {
	var problems []string
	for _, side := range []struct {
		role   string
		server serverFacts
	}{{"source", source.Server}, {"target", target.Server}} {
		if side.server.Major < minMajorVersion {
			problems = append(problems, fmt.Sprintf("the %s runs SQL Server %s; backing up to S3 needs SQL Server 2022 (16.x) or later",
				side.role, orUnknown(side.server.Version)))
		}
		if !side.server.Sysadmin {
			problems = append(problems, fmt.Sprintf("the %s user must be a member of the sysadmin role", side.role))
		}
	}
	if source.Server.Major > target.Server.Major && target.Server.Major >= minMajorVersion {
		problems = append(problems, fmt.Sprintf("the target runs SQL Server %s, older than the source (%s); a backup cannot be restored on an older version",
			target.Server.Version, source.Server.Version))
	}
	if source.Server.Name != "" && source.Server.Name == target.Server.Name && source.Server.StartedAt == target.Server.StartedAt {
		problems = append(problems, "the source and the target are the same server")
	}
	if target.Server.Hadr && target.Server.Role != "" && target.Server.Role != "PRIMARY" {
		problems = append(problems, "the target connection reaches a secondary replica; it must reach the availability group's primary")
	}

	var missing, offline, notFull, keys []string
	for _, name := range options.Databases {
		facts, found := findDatabase(source.Databases, name)
		switch {
		case !found:
			missing = append(missing, name)
		case facts.State != "ONLINE":
			offline = append(offline, fmt.Sprintf("%s (%s)", name, facts.State))
		case facts.RecoveryModel != "FULL":
			notFull = append(notFull, name)
		}
		if found && facts.Encrypted && facts.Thumbprint != "" && !slices.Contains(target.Certificates, facts.Thumbprint) {
			keys = append(keys, name)
		}
		if found && facts.InAG && options.LeaveSourceRestoring {
			problems = append(problems, fmt.Sprintf("database %s is in an availability group on the source, which cannot be left restoring; "+
				"turn off \"Leave the source databases restoring\"", name))
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "databases not found on the source: "+strings.Join(missing, ", "))
	}
	if len(offline) > 0 {
		problems = append(problems, "databases not online on the source: "+strings.Join(offline, ", "))
	}
	if len(notFull) > 0 {
		problems = append(problems, "these databases do not use the FULL recovery model, which log backups need: "+strings.Join(notFull, ", ")+
			" (ALTER DATABASE … SET RECOVERY FULL)")
	}
	if len(keys) > 0 {
		problems = append(problems, "these databases use transparent data encryption; create the certificate that protects their key on the target first: "+
			strings.Join(keys, ", "))
	}

	var existing, inAG []string
	for _, facts := range target.Existing {
		existing = append(existing, facts.Name)
		if facts.InAG {
			inAG = append(inAG, facts.Name)
		}
	}
	switch {
	case len(inAG) > 0:
		problems = append(problems, "these databases exist on the target in an availability group; remove them from it and drop them first: "+
			strings.Join(inAG, ", "))
	case len(existing) > 0 && !options.RecreateDatabases:
		problems = append(problems, "these databases already exist on the target: "+strings.Join(existing, ", ")+
			"; drop them or enable \"Recreate the target databases\"")
	}
	if len(source.Recent) > 0 && !options.IgnoreRecentLogBackups {
		listed := make([]string, 0, maxListedBackups)
		for _, backup := range source.Recent[:min(len(source.Recent), maxListedBackups)] {
			listed = append(listed, backup.String())
		}
		problems = append(problems, "log backups were taken on the source in the last 24 hours ("+strings.Join(listed, "; ")+
			"); another log backup during the migration breaks its log chain. Disable them (for an OpenEverest instance: point-in-time recovery) "+
			"and enable \"Other log backups are disabled\"")
	}
	return problems
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
