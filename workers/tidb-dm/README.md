# tidb-dm worker

The worker image for the `tidb-dm` engine. It migrates MySQL databases into an
OpenEverest TiDB instance with
[TiDB Data Migration](https://docs.pingcap.com/tidb/stable/dm-overview/)
(DM): DM dumps the databases, loads them into TiDB, then replicates the
source's binary log until the cutover.

The image is `pingcap/dm:v8.5.8` (dm-master, dm-worker and dmctl) plus a Go
agent. The agent runs dm-master and dm-worker as child processes on the
loopback (`127.0.0.1:8261`, `:8291`, `:8262`), restarts them when they exit,
and drives them through dm-master's HTTP gateway (`/apis/v1alpha1`). It runs
as `65532:65532` and listens on `:2243`.

## Volume

`DATA_DIR` (`/data`) is the migration's PersistentVolumeClaim:

| Path | Content |
| --- | --- |
| `dm-master/` | dm-master's configuration and embedded etcd: DM's sources, tasks and their stages |
| `dm-worker/` | dm-worker's configuration (relay logs are not used) |
| `dump/<name>/` | DM's dump of the databases, deleted once loaded (`clean-dump-file`) |

DM has no streaming copy, so the volume must hold the dump: about the size of
the source's data. With the volume, a restarted pod continues where DM
stopped: the dump starts again, the load continues from its checkpoints, and
replication from its saved position.

## Flow

1. **Preflight**, every problem at once:
   - Source: MySQL 5.7 to 8.4 (not MariaDB, not TiDB), `log_bin`,
     `binlog_format=ROW`, `binlog_row_image=FULL`,
     `binlog_transaction_compression=OFF`, a non-zero `server_id`, and the
     databases exist. Tables whose collations TiDB lacks, spatial columns and
     foreign keys with `CASCADE`/`SET NULL`/`SET DEFAULT` actions are listed
     (MySQL does not log the changes those actions make, and DM applies
     changes with `foreign_key_checks=0`).
   - Target: TiDB, and the databases have no tables (unless
     `recreateDatabases`).
   - The source's data fits the volume.
   - DM's precheck (`dmctl check-task`): privileges, connections and the
     rest. Items the agent checks itself are not repeated.
   Stored routines, triggers, events and FULLTEXT indexes, which TiDB does not
   have, are reported in the status, not as problems.
2. **State** on TiDB: `openeverest_migration.dm_state` holds the phase
   (`preflight`, `copying`, `replicating`, `completed`), the options, the
   pause and the agent's own failure. A failed preflight is recorded, so a
   restarted worker waits for a resume.
3. **Heartbeat**: the database `openeverest_<name>` on the source, with one
   row written every 5s. It exists before the dump, so DM copies and
   replicates it; its value on TiDB gives the lag with the source's clock only.
4. **DM**: the source `<name>` (`enable-gtid` when `gtid_mode=ON`) and the task
   `<name>` in `all` mode, `collation_compatible: strict`, `do-dbs` = the
   databases and the heartbeat database, `ignore-tables` = the excluded tables
   (`do-tables` is not used: DM skips `ignore-tables` for tables it matches),
   `checkpoint-flush-interval: 5`. DM's checkpoints are in `dm_meta.<name>_*`
   on TiDB. The task starts with `ignore-checking-items: ["all"]`: the
   preflight ran DM's precheck already.
5. **Supervision** every heartbeat:
   - Records the `replicating` phase once DM's sync unit runs, and creates the
     views (DM neither dumps nor replicates them) from the source's
     `SHOW CREATE VIEW`, with `CURRENT_USER` as definer.
   - Starts the task again if DM has none (the volume was lost), without DM's
     precheck. DM continues from its checkpoints once its load finished
     (`dm_meta.<name>_lightning_checkpoint_list`) and copies from the
     beginning if the load never began; a load interrupted with the dump
     gone fails the migration.
   - Skips a DDL statement TiDB cannot parse (DM error 36067) when
     `SHOW BINLOG EVENTS` shows a `CREATE`/`ALTER` `TABLE`/`DATABASE` of a
     database outside the migration that names no migrated database. DM parses
     DDL before it filters, so such statements would stop the task.
6. **Cutover**: reads the source's position (`SHOW BINARY LOG STATUS`, or
   `SHOW MASTER STATUS` before MySQL 8.2) and waits until DM's saved
   checkpoint (its status' `syncerBinlog`/`syncerBinlogGtid`) contains it: DM
   saves a checkpoint only after every earlier change was applied. Then it
   creates the views again, removes DM's task and source, drops the heartbeat
   databases and DM's checkpoints, and records `completed` with the notes.

## HTTP API

| Route | Description |
| --- | --- |
| `GET /healthz` | Liveness and readiness. |
| `GET /status` | State, message, error, bytes dumped/loaded, lag and `initialSyncCompleted`. |
| `POST /start` | Starts the migration with the JSON options below. Runs in the background. |
| `POST /pause` · `/resume` | Pauses or resumes DM's task. Resume also runs a failed preflight again, restarts a task DM lost, resumes a task DM stopped on an error, and retries a failed cutover. |
| `POST /cutover` | Runs the cutover in the background. A paused migration is resumed first. |
| `POST /teardown` | Removes DM's task and source, DM's checkpoints, the heartbeat databases and the state row, then `dm_meta` once no task has tables in it, `lightning_task_info` if it records nothing, and an empty `openeverest_migration`. Data on TiDB stays. Idempotent. |

Start options: `databases` (required), `excludeTables` (`database.table`),
`recreateDatabases`, `storageGiB`, `copyThreads` (1-32) and `syncThreads`
(1-64).

## Environment

| Variable | Description |
| --- | --- |
| `SOURCE_URI` | `mysql://user:password@host:port/?ssl-mode=…&ssl-ca=…` of the source. |
| `TARGET_URI` | The TiDB instance (its SQL Service, or TiProxy). |
| `MIGRATION_NAME` | DM's source and task name (`mig_<id>`, at most 52 characters); also names the heartbeat database. |
| `DATA_DIR` | The worker volume (default `/data`). |
| `DM_BIN_DIR` | Where `dm-master`, `dm-worker` and `dmctl` are (default `/`). |
| `HEARTBEAT_INTERVAL` | Heartbeat and supervision period (default `5s`). |

DM either verifies a server against the CA (`ssl-mode` `VERIFY_CA` or
`VERIFY_IDENTITY`; DM always checks the host name) or connects without TLS
(`DISABLED`, `PREFERRED`). The backend refuses `REQUIRED` without a CA.

## Limitations

- User accounts and grants are not copied. Create the application users on
  TiDB before the cutover.
- A DDL statement TiDB cannot run on a migrated table stops the migration;
  make the source compatible and create the migration again.
- The source keeps binary logs on its own schedule; if the copy or a pause
  outlasts `binlog_expire_logs_seconds` (or RDS's binlog retention hours), DM
  cannot continue and the migration must be created again.
- Tables excluded from the migration must not be referenced by foreign keys
  of migrated tables.

## Development

```bash
go test ./...
docker build -t plugin-migrations-tidb-dm:dev .
```
