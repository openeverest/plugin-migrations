# mysql-replication worker

The worker image for the `mysql-replication` and `mariadb-replication`
engines. It migrates MySQL (or MariaDB) databases into an OpenEverest MySQL
(or MariaDB) instance with **native GTID replication**. MySQL Shell (MySQL) or
mydumper/myloader (MariaDB) copies the data; then the target server replicates
from the source over its own replication channel until the cutover. The worker
moves no rows after the copy: the target server reads the source's binary log
directly.

The image is `mysql:8.4` (for MySQL Shell, `mysqlsh`; the server in it is
never started) with mydumper/myloader, plus a Go agent. It runs as the
`mysql` user (999:999) and listens on `:2243`. `FLAVOR` selects MySQL or
MariaDB; the lifecycle below is shared, and [MariaDB](#mariadb) lists what
differs.

## Flow (MySQL)

1. **Preflight**. Source: MySQL 8.0+ (not MariaDB), binary logging,
   `gtid_mode=ON`, `binlog_format=ROW`, a user with `REPLICATION SLAVE`, and
   the databases exist. Target: MySQL 8.0.23+, the same or a newer version
   than the source, `gtid_mode=ON`, `skip_replica_start=ON`, and the databases
   do not exist yet (unless `recreateDatabases`). On PXC every migrated table
   must be InnoDB with a primary key. Every problem is reported at once.
2. **Replica node**. On PXC, `wsrep_node_incoming_address` names the node
   behind the proxy (for example
   `orders-pxc-0.orders-pxc.shop.svc.cluster.local:3306`). The agent pins the
   migration to it and talks to it directly from then on: the copy's GTIDs
   (`gtid_purged`) and the channel exist only on that node.
3. **Heartbeat and state**. An `openeverest_migration` database on both sides.
   Each migration has its own `heartbeat_mig_<id>` table, written every 5s on
   the source; its replicated value on the target gives the lag, measured with
   the source clock only. Its channel replicates only that table, so several
   migrations can share a source. The target's `state` table records the
   replica node, the options and the phase. A restarted worker reads it,
   retrying until the target answers; until then it reports that it is
   connecting and accepts no start. The source row is created before the copy
   and the agent seeds the target row, because the snapshot does not include
   it.
4. **Copy**. `util.copySchemas` streams a consistent snapshot of the
   databases into the replica node with parallel threads (no intermediate
   files), including views, routines, triggers and events. `local_infile` is
   enabled on the node for the copy only. Copied events are set to
   `DISABLE ON REPLICA` so they do not run on the target before the cutover.
5. **Replication**. The GTIDs of the snapshot the target does not have yet
   are appended to `gtid_purged`, then the channel `mig_<id>` is created with
   `SOURCE_AUTO_POSITION`, `REQUIRE_ROW_FORMAT` and these filters:
   `REPLICATE_WILD_DO_TABLE` = each `database.%` and the migration's heartbeat table,
   `REPLICATE_IGNORE_TABLE` = the excluded tables. DDL replicates too.
6. **Supervision**. Every heartbeat the agent checks the channel. MySQL does
   not persist channel filters, and PXC runs with `skip_replica_start=ON`, so
   after a node restart the channel is stopped without filters: the agent
   restores the filters and starts it. A transaction that fails with *Unknown
   database* for a database that is not migrated (for example `CREATE
   PROCEDURE other.p`, which no table filter matches) is skipped with an empty
   transaction. Other errors are reported and retried every minute.
7. **Cutover**. The agent reads the source's `gtid_executed` and waits
   (`WAIT_FOR_EXECUTED_GTID_SET`) until the target applied it, so every change
   committed before the cutover is on the target. It then removes the channel
   (`RESET REPLICA ALL`), gives every event the status it has on the source
   (keeping its definer), and removes the heartbeat on both sides. An event
   that replicated with a definer the target user cannot change (one holding
   `SYSTEM_USER`, such as `root@localhost`) is listed in the completion
   message, to enable with a privileged account.
8. **Definers**. MySQL lets only a `SYSTEM_USER` account create or change
   objects whose definer holds `SYSTEM_USER`. OpenEverest's PXC `root@%` does
   not, while `root@localhost` (a common definer) does. Views, routines,
   triggers and events with such a definer are left out of the copy and
   recreated by the agent from `SHOW CREATE` with `DEFINER=CURRENT_USER`, the
   same SQL mode, character set and time zone; `SQL SECURITY` is unchanged.

## MariaDB

With `FLAVOR=mariadb` the same flow runs with MariaDB's statements:

- **Preflight**: source MariaDB 10.6+, target 10.11+ of the same or a newer
  series; `REPLICATION SLAVE` is read from `SHOW GRANTS` (grants through roles
  are not seen). MariaDB does not expose `skip-slave-start` as a variable, so
  the backend passes `TARGET_SKIP_REPLICA_START=true` only for targets whose
  provider starts them with it; without it the preflight fails. Both flavors
  require different `server_id`s on the source and the target.
- **Replica node**: on Galera, the entry of `wsrep_cluster_address` named
  after `wsrep_node_name` (`wsrep_node_incoming_address` is usually `AUTO`).
  `gtid_slave_pos` is per node, like `gtid_purged` on PXC.
- **Copy**: `mydumper --stream` (consistent through `BACKUP STAGE
  BLOCK_COMMIT`, with routines, events and triggers) is piped into `myloader
  --stream` through the agent, which counts the data files for the progress
  and reads the snapshot's `executed_gtid_set` from the final `metadata` file.
  Credentials go to `0600` option files in the work directory. Copied events
  are set to `DISABLE ON SLAVE`.
- **Replication**: `SET GLOBAL gtid_slave_pos`, then `CHANGE MASTER 'mig_<id>'
  TO … MASTER_USE_GTID=slave_pos`. The filters are the connection's
  `mig_<id>.replicate_wild_do_table` and `mig_<id>.replicate_ignore_table`
  variables, also not persisted across restarts.
- **Supervision**: an *Unknown database* failure for a database that is not
  migrated is skipped with `sql_slave_skip_counter` on the connection.
- **Galera and account statements**: MariaDB begins Galera's total order
  isolation for `CREATE/ALTER/DROP USER`, roles, `GRANT` and `REVOKE` before
  it applies the replication filters, so such statements from the source are
  skipped on the replica node but run on the other nodes. Accounts must not
  change on the source during a migration into Galera.
- **Cutover**: the source's `@@gtid_binlog_pos`, waited for with
  `MASTER_GTID_WAIT`; the connection is removed with `RESET SLAVE 'mig_<id>'
  ALL`. Definers are kept (MariaDB has no `SYSTEM_USER`). On Galera, events
  then run only on the replica node; the other nodes show them as
  `SLAVESIDE_DISABLED`, as for any event created on a Galera node.

## HTTP API

| Route | Description |
| --- | --- |
| `GET /healthz` | Liveness and readiness. |
| `GET /status` | Phase, message, error, bytes copied/total, lag and `initialSyncCompleted`. |
| `POST /start` | Starts the migration with the JSON options below. Runs in the background. |
| `POST /pause` · `/resume` | `STOP REPLICA` / `START REPLICA` for the channel. A channel stopped by an error is restarted by the agent every minute, so it resumes once the cause is fixed. |
| `POST /cutover` | Runs the cutover in the background. A paused migration is resumed first. |
| `POST /teardown` | Removes the channel and the `openeverest_migration` database on the target, and the migration's heartbeat table on the source (and the database once empty). Data on the target stays. Idempotent. |

Start options: `databases` (required), `excludeTables` (`database.table`),
`recreateDatabases` and `copyThreads` (1-32, default 4).

## Environment

| Variable | Description |
| --- | --- |
| `SOURCE_URI` | `mysql://user:password@host:port/?ssl-mode=…&ssl-ca=…` of the source. The target server connects with the same user and host for replication. |
| `TARGET_URI` | The target, usually its proxy (HAProxy or ProxySQL). |
| `REPLICATION_NAME` | Channel name (`mig_<id>`, at most 54 characters); also names the heartbeat table. |
| `FLAVOR` | `mysql` (default) or `mariadb`. |
| `TARGET_SKIP_REPLICA_START` | `true` when the MariaDB target is known to start with `skip-slave-start` (required for `mariadb`). |
| `HEARTBEAT_INTERVAL` | Heartbeat and supervision period (default `5s`). |

`ssl-mode` uses MySQL client names (`DISABLED`, `PREFERRED`, `REQUIRED`,
`VERIFY_CA`, `VERIFY_IDENTITY`). The replication channel uses TLS unless the
source is `DISABLED`, but it cannot verify the source certificate: the CA file
exists only in the worker.

## Limitations

- User accounts and grants are not copied. Create the application users on
  the target before the cutover.
- The target server must reach the source over the network.
- The source keeps binary logs on its own schedule; if the copy or a pause
  outlasts `binlog_expire_logs_seconds` (or RDS's binlog retention hours), the
  channel cannot continue and the migration must be created again.
- Tables excluded from the migration must not be referenced by foreign keys
  of migrated tables.
- Galera (PXC and MariaDB) rejects writesets larger than `wsrep_max_ws_size`
  (2 GiB), so a single source transaction cannot be larger.

## Development

```bash
go test ./...
docker build -t plugin-migrations-mysql-replication:dev .
```
