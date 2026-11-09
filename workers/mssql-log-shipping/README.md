# mssql-log-shipping worker

The worker image for the `mssql-log-shipping` engine. It migrates SQL Server
databases into an OpenEverest `mssql` instance (provider-mssql) with log
shipping through S3: the SQL Servers back up to and restore from an
OpenEverest BackupStorage themselves (`BACKUP … TO URL = 's3://…'`), and the
agent drives them with T-SQL. Both servers need SQL Server 2022 (16.x) or
later.

The image is distroless with the agent (`mssql-log-shipping-agent`) only. It
runs as 65532:65532 and listens on `:2243`.

## Configuration

| Variable | Notes |
| --- | --- |
| `MIGRATION_NAME` | The migration ID; names the S3 prefix, the credential and the state rows. |
| `SOURCE_URI`, `TARGET_URI` | `sqlserver://user:password@host:port?encrypt=…&TrustServerCertificate=…&certificate=…` (Secret). |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_FORCE_PATH_STYLE` | From the BackupStorage. The endpoint must be `https://`. |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | From the BackupStorage's credentials Secret (Secret). |
| `S3_CA_FILE` | The credentials Secret's optional `ca.crt`, for the agent's own S3 calls. |

Backups go under `openeverest-migrations/<migration>/` in the bucket. The
servers authenticate with a credential named after that prefix
(`s3://<endpoint>/<bucket>/openeverest-migrations/<migration>`): SQL Server
picks the credential with the longest matching prefix, so it never collides
with the operator's bucket-wide backup credential.

## Flow

1. **Preflight** (every problem at once): both servers run SQL Server 2022+;
   the target is not older than the source; both users are `sysadmin`;
   source and target are different servers; the databases exist, are online
   and use the FULL recovery model; TDE databases have their certificate on
   the target; the target databases do not exist (or `recreateDatabases`),
   and are not in an availability group; no log backup outside the migration
   ran on the source in the last 24 hours (unless `ignoreRecentLogBackups`;
   backups of OpenEverest migrations that no longer run, whose credential is
   gone, do not count);
   the target connection reaches the availability group's primary; databases
   in an availability group cannot be left restoring. The agent then writes a
   probe object and makes both servers read it with the migration's
   credential: they must trust the storage's TLS certificate (provider-mssql
   mounts the BackupStorage `ca.crt` into instances the storage is attached
   to).
2. **State**. `master.dbo.openeverest_migration` and
   `master.dbo.openeverest_migration_database` on the target hold the phase
   (`preparing`, `copying`, `shipping`, `cutover`, `completed`), the failure
   (and whether a resume may retry it), and per database the last backup
   (sequence, LSN range, the source's clock when it started) and whether it
   was restored. master is used because the operator adopts every online
   user database into an availability group. A restarted worker continues
   from there: a failure stays failed until resumed, and the agent never
   drops a database on its own after the preflight.
3. **Copy**. A full backup of each database (`COPY_ONLY`, so the source's
   differential base is untouched; a regular one when the database never had
   a full backup, which log backups need), striped over one URL per 75 GiB,
   is restored `WITH NORECOVERY, REPLACE` on the target with every file moved
   to the target's default data and log paths.
4. **Log shipping**. Every `logBackupIntervalSeconds` (default 10), each
   database gets a log backup that is restored at once. Before each backup the
   agent reads `msdb` for log backups that are not the migration's: one
   newer than the last restored log breaks the chain, and the migration fails
   with the backup's device and time. A backup whose first LSN is past the
   last restored LSN fails it too (another replica's history). Restored
   backups are deleted from the bucket. A backup that a restarted worker
   finds in the bucket but not in its state is restored, not taken again,
   when it continues the chain (otherwise it is deleted).
5. **Lag**. The source's clock at the start of the last restored backup of
   each database, against the source's clock now: everything committed before
   that start is on the target. The cutover is offered once every database
   restored a log backup and the lag is 15s or less.
6. **Pause** stops taking backups (the source keeps its log meanwhile).
7. **Cutover**. Each database gets a final log backup, started after the
   request so it holds every transaction committed before it
   (`leaveSourceRestoring`: a tail-log backup `WITH NORECOVERY` after `SET
   SINGLE_USER`, so the source takes no more writes), which is restored; then
   the databases are recovered and opened to all users. On an availability
   group the agent requires the replica it restored on to still be the
   primary; the operator then adds the databases to the group and seeds the
   secondaries. With `copyLogins`, the SQL logins of the databases' users and
   owners are created with the same SID and password hash on every replica
   (Windows logins and server roles are not; users whose login the source
   server lacks are listed in the notes). Finally the credentials, on every
   replica of both servers' availability groups, and the bucket's objects are
   removed.
8. **Teardown** removes the credentials on both servers, the bucket's
   objects and the state rows (and the tables once empty). Databases on the
   target stay, restoring if the migration did not complete: drop them, or
   bring them online with `RESTORE DATABASE … WITH RECOVERY`. A source left
   restoring by the cutover is brought back the same way, then `ALTER
   DATABASE … SET MULTI_USER` (the tail-log backup ran in single-user mode).

## Availability groups

- **Source**: backups run on the replica the connection reaches (the
  primary). Credentials are server objects, so every cycle creates the
  migration's credential where it is missing: after a failover the chain
  continues on the new primary.
- **Target**: the agent pins itself to the primary replica (the host of its
  `endpoint_url`) because the restoring databases exist only there. If that
  replica becomes a secondary before the cutover's final backups, the agent
  records the migration on the new primary, drops its restoring copies on the
  old replica and copies the databases again (the restored log backups are
  gone from the bucket); the phase goes back to the full load. After the
  final backups it refuses to recover anywhere but the original replica. A
  restarted worker recovers the newest state it finds on the group's
  replicas.

## Development

```bash
go test ./...
docker build -t plugin-migrations-mssql-log-shipping:dev .
```
