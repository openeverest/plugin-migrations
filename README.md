# OpenEverest Migrations

`plugin-migrations` moves databases **into** OpenEverest with minimal downtime,
much like AWS DMS. A per-migration worker copies existing data (**full load**),
keeps the target in sync with ongoing changes (**CDC**), and you trigger
**cutover** when your application is ready to switch.

> **Status: v0.1 draft.** Engines: **MongoDB → MongoDB** built on
> [Percona ClusterSync for MongoDB](https://github.com/percona/percona-clustersync-mongodb)
> (PCSM), **PostgreSQL → PostgreSQL** built on native logical replication,
> **MySQL → MySQL** and **MariaDB → MariaDB** built on native GTID replication,
> **Redis / Valkey → Valkey** built on
> [RedisShake](https://github.com/tair-opensource/RedisShake), and
> **SQL Server → SQL Server** built on log shipping through S3.
> The plugin appears in the OpenEverest **sidebar** only.

Read [docs/architecture.md](docs/architecture.md) for the design, the engine
contract, and the HTTP API.

## How it works

```
Browser ─▶ OpenEverest proxy ─▶ plugin backend ─▶ OpenEverest API (your JWT: instances, connection details)
                                     │
                                     └─▶ Kubernetes (workload namespace)
                                           ConfigMap  mig-xxxx   ← migration record (no credentials)
                                             ├─ Secret      mig-xxxx   ← endpoint URIs / CAs
                                             ├─ Deployment  mig-xxxx   ← engine worker (e.g. PCSM)
                                             └─ Service     mig-xxxx   ← worker control API
```

1. **Create**: choose an engine, a source (an OpenEverest instance or an
   external URI), and a target (an OpenEverest instance). The backend reads the
   instances' connection details with *your* permissions, stores the
   credentials in a Secret, and starts the worker.
2. **Full load → CDC**: the backend reconciler starts the migration as soon as
   the worker is ready, then reports progress (bytes copied, lag, events
   applied).
3. **Cutover**: stop writes on the source, wait for lag ≈ 0, then click
   **Cutover**. The engine finalizes the target (PCSM: indexes and other
   catalog work; PostgreSQL: sequences, materialized views, and removing the
   replication slot and publication from the source; MySQL and MariaDB:
   waiting for the source's GTID position, removing the replication channel,
   and enabling events; Redis/Valkey: waiting until the target has every
   change and stopping RedisShake; SQL Server: restoring a final log backup
   and recovering the databases).
4. **Delete**: removes the worker. Data on the target is kept. Engines that
   leave replication state behind (PostgreSQL) clean it up first.

The backend has no database. Migration records are ConfigMaps, so migrations
survive backend restarts and upgrades. Worker progress is checkpointed by the
engine itself (PCSM stores checkpoints in the target cluster; PostgreSQL keeps
the replication state in the target's subscription and the source's slot;
MySQL and MariaDB keep it in the target's replication channel and GTID
position).

## Engines

| Engine | Source | Target | Worker image |
| --- | --- | --- | --- |
| `pcsm` | MongoDB (OpenEverest `percona-server-mongodb` instance or external URI) | OpenEverest `percona-server-mongodb` instance | `percona/percona-clustersync-mongodb:1.0.0` |
| `postgres-logical` | PostgreSQL 13+ (OpenEverest instance or external URI) | OpenEverest `percona-postgresql` instance (PostgreSQL 15+) | `ghcr.io/openeverest/plugin-migrations-postgres-logical` |
| `mysql-replication` | MySQL 8.0+ / Percona Server / PXC (OpenEverest `percona-xtradb-cluster` instance or external URI) | OpenEverest `percona-xtradb-cluster` instance (8.0.23+) | `ghcr.io/openeverest/plugin-migrations-mysql-replication` |
| `mariadb-replication` | MariaDB 10.6+ (OpenEverest `mariadb` instance or external URI) | OpenEverest `mariadb` standalone or Galera instance (10.11+, provider-mariadb 0.2.2+) | same image as `mysql-replication` |
| `redis-shake` | Redis 2.8+ / Valkey, standalone or cluster (OpenEverest `valkey` instance or external URI) | OpenEverest `valkey` instance | `ghcr.io/openeverest/plugin-migrations-redis-shake` |
| `mssql-log-shipping` | SQL Server 2022+ (OpenEverest `mssql` instance or external URI) | OpenEverest `mssql` instance (2022+, standalone or availability group) | `ghcr.io/openeverest/plugin-migrations-mssql-log-shipping` |

PCSM options include namespace include/exclude filters (`db`, `db.*`,
`db.collection`), pausing once the initial sync completes, and clone/replication
worker tuning.

`postgres-logical` migrates one database. The worker copies the schema with
`pg_dump`/`pg_restore`, then lets PostgreSQL do the rest: the **target**
subscribes to a publication on the source, copies the tables and streams
changes server-to-server. Options: the source database, schema include/exclude
filters, table excludes, and *recreate the target database*. See
[workers/postgres-logical/README.md](workers/postgres-logical/README.md) and the
options tables in [docs/architecture.md](docs/architecture.md) (§8).

`mysql-replication` migrates one or more databases. MySQL Shell copies a
consistent snapshot (tables, views, routines, triggers and events), then the
**target** replicates from the source over a GTID replication channel, schema
changes included. Options: the databases, table excludes, *recreate the target
databases*, and the number of copy threads. See
[workers/mysql-replication/README.md](workers/mysql-replication/README.md).

`mariadb-replication` works the same way and takes the same options; the same
worker copies with mydumper/myloader (streamed, no disk for the data) and
replicates over a named MariaDB replication connection
(`MASTER_USE_GTID=slave_pos`). It connects to OpenEverest instances with the
admin (root) connection provider-mariadb publishes.

`redis-shake` migrates every key of database 0, standalone or cluster.
RedisShake copies a snapshot through the source's replication protocol, then
applies the changes the source streams. Options: key prefixes to exclude and
*delete all keys on the target*. It cannot pause, and an interrupted run
starts again from the beginning. See
[workers/redis-shake/README.md](workers/redis-shake/README.md).

`mssql-log-shipping` migrates one or more databases with log shipping
through an OpenEverest **BackupStorage** (S3) you pick in the target's
namespace: the source backs up each database to S3 and the target restores it
`WITH NORECOVERY`, then a log backup is taken and restored every few seconds.
The cutover restores a final log backup, taken after the request, and
recovers the databases; with *Copy logins* it also creates the databases'
SQL logins with the same SID and password. Options: the databases, *recreate
the target databases*, *copy logins*, *leave the source databases
restoring*, and the log backup interval. See
[workers/mssql-log-shipping/README.md](workers/mssql-log-shipping/README.md).

### Adding an engine

1. Backend: implement `engine.Engine` in `backend/internal/engine/<id>/`
   (`Prepare` builds the worker spec; `Controller` drives the running worker
   and maps its state onto the shared phases; workers built on the agent API
   in `workers/` reuse `engine/agentclient`; set `CanPause` in the descriptor
   when a migration can be paused and resumed where it stopped, and
   `RequiresBackupStorage` when it stages data in a BackupStorage: the backend
   then resolves the one the user picks and passes it to `Prepare`). Register its
   constructor in `compiledEngines` in `backend/main.go`.
2. Helm: add `engines.<id>` (enabled flag and image) to `values.yaml`.
3. Frontend: add `src/engines/<id>/` (label, compatible providers, default
   options, options form) and register it in `src/engines/index.ts`.

The lifecycle, storage, API, and pages are shared and need no changes.

## Install

```bash
helm install plugin-migrations oci://ghcr.io/openeverest/charts/plugin-migrations \
  --version 0.1.0 -n everest-system
```

Then open **Migrations** in the OpenEverest sidebar.

### Configuration (`values.yaml`)

| Parameter | Description | Default |
| --- | --- | --- |
| `engines.pcsm.enabled` | Enable the MongoDB (PCSM) engine | `true` |
| `engines.pcsm.image.*` | PCSM worker image | `percona/percona-clustersync-mongodb:1.0.0` |
| `engines.postgres-logical.enabled` | Enable the PostgreSQL logical replication engine | `true` |
| `engines.postgres-logical.image.*` | PostgreSQL worker image | `ghcr.io/openeverest/plugin-migrations-postgres-logical` (chart version) |
| `engines.mysql-replication.enabled` | Enable the MySQL replication engine | `true` |
| `engines.mysql-replication.image.*` | MySQL worker image | `ghcr.io/openeverest/plugin-migrations-mysql-replication` (chart version) |
| `engines.mariadb-replication.enabled` | Enable the MariaDB replication engine | `true` |
| `engines.mariadb-replication.image.*` | MariaDB worker image (the MySQL worker image) | `ghcr.io/openeverest/plugin-migrations-mysql-replication` (chart version) |
| `engines.redis-shake.enabled` | Enable the Redis / Valkey engine | `true` |
| `engines.redis-shake.image.*` | Redis / Valkey worker image | `ghcr.io/openeverest/plugin-migrations-redis-shake` (chart version) |
| `engines.mssql-log-shipping.enabled` | Enable the SQL Server log shipping engine; also grants the backend `get` on Secrets cluster-wide, to read the chosen BackupStorage's credentials | `true` |
| `engines.mssql-log-shipping.image.*` | SQL Server worker image | `ghcr.io/openeverest/plugin-migrations-mssql-log-shipping` (chart version) |
| `workload.namespace` | Namespace for migration records and workers | release namespace |
| `workload.createNamespace` | Let Helm create `workload.namespace` | `false` |
| `worker.resources` | Resources for every worker | `500m`/`1Gi` requests, `4Gi` memory limit |
| `worker.nodeSelector`, `worker.tolerations` | Worker placement | `{}`, `[]` |
| `worker.imagePullSecrets` | Pull secrets for worker images | `[]` |
| `reconcileInterval` | Status refresh / auto-start interval | `5s` |
| `everestAPIURL` | OpenEverest API URL | auto-discovered |

## Requirements and limitations (v0.1)

- External sources must be reachable from worker pods in the workload
  namespace.
- Each target instance can have only one migration at a time, even a
  completed one: delete the previous migration before starting a new one.
- The target's connection user (PSMDB `databaseAdmin`) must have the
  privileges PCSM needs. Sharded targets are not validated yet; see the open
  questions in [docs/architecture.md](docs/architecture.md) (§11).
- Deleting a migration does not remove PCSM's bookkeeping database from the
  target.

PostgreSQL (`postgres-logical`):

- Source: PostgreSQL 13+ with `wal_level = logical`, and a user with
  `REPLICATION` (or superuser / `rds_replication`) that can read the migrated
  tables. Every migrated table needs a primary key or a replica identity;
  preflight refuses tables without one, because publishing them would make
  `UPDATE`/`DELETE` fail on the source.
- Target: an OpenEverest `percona-postgresql` instance (its connection user is
  a superuser, which `CREATE SUBSCRIPTION` needs), same or newer major version.
- The **target instance** connects to the source, not only the worker pod, so
  the source must be reachable from the target's pods.
- DDL is not replicated: freeze schema changes until cutover. Sequences and
  materialized views are synced at cutover. Large objects are not copied.
- The target database must not exist, or enable *Recreate the target
  database* to drop it first. Schemas the operator creates in every database
  (`pgbouncer`) are not copied.
- The source keeps WAL for the replication slot while the migration runs, also
  while it is paused. Cut over or delete the migration to release it. If the
  source is gone, delete with **Delete anyway** (API: `DELETE
  /api/migrations/{id}?force=true`) and drop the slot yourself.

MySQL (`mysql-replication`):

- Source: MySQL 8.0+ (Percona Server, PXC, RDS/Aurora MySQL; not MariaDB) with
  binary logging, `gtid_mode=ON` and `binlog_format=ROW`, and a user with
  `REPLICATION SLAVE` that can read the migrated databases (`SELECT`, `SHOW
  VIEW`, `TRIGGER`, `EVENT`, and `RELOAD` or `BACKUP_ADMIN` for the consistent
  snapshot) and create the `openeverest_migration` heartbeat database.
- Target: an OpenEverest `percona-xtradb-cluster` instance (8.0.23+), the same
  or a newer version. Every migrated table must be InnoDB with a primary key;
  preflight lists the tables that are not.
- The **target instance** connects to the source, not only the worker pod.
- Schema changes replicate, so they need not be frozen. User accounts and
  grants are not copied: create the application users on the target.
- The databases must not exist on the target, or enable *Recreate the target
  databases*.
- The source keeps binary logs on its own schedule. If the copy or a pause
  outlasts its retention, the migration fails and must be created again.
- The source and the target must have different `server_id`s: a replica
  ignores changes carrying its own.

MariaDB (`mariadb-replication`):

- Source: MariaDB 10.6+ with binary logging and `binlog_format=ROW` (MariaDB
  defaults to `MIXED`), and a user with `REPLICATION SLAVE`, `BINLOG MONITOR`,
  `RELOAD` and read access to the migrated databases. For an OpenEverest
  `mariadb` source, set `binlog_format=ROW` in its configuration; standalone
  and Galera instances also need `log_bin` (replication-topology instances
  have it).
- The source and the target must have different `server_id`s. OpenEverest
  standalone and Galera instances use `server_id=1` (replication-topology
  instances 10, 11, …), so a standalone or Galera source needs another
  `server_id` in its configuration.
- Target: an OpenEverest `mariadb` **standalone or Galera** instance (10.11+,
  the same or a newer series than the source) of provider-mariadb 0.2.2 or
  later, which starts MariaDB with `skip-slave-start` and publishes the admin
  connection. Replication-topology targets are not supported. On Galera every
  migrated table must be InnoDB with a primary key.
- The **target instance** connects to the source, not only the worker pod.
- Schema changes replicate. User accounts are not copied.
- **Galera targets: do not create, change or drop users, roles or grants on
  the source while the migration runs.** MariaDB applies the replication
  filters to these statements only after Galera has already sent them to the
  other nodes (total order isolation starts first), so they are skipped on the
  replica node but executed on the others, and the nodes' accounts diverge.
  An OpenEverest source runs such statements itself when its users change
  (mariadb-operator recreates or alters them). Table and database DDL is not
  affected, nor are standalone targets or PXC.
- On Galera, scheduled events run only on the node that enabled them (the
  node the migration replicated into); the other nodes show them as
  `SLAVESIDE_DISABLED`, as for any event created on a Galera node.

Redis / Valkey (`redis-shake`):

- Source: Redis 2.8+ or Valkey, standalone (the primary) or a cluster (any
  primary). The user needs `PSYNC` (replication) and must be able to write a
  heartbeat key. Only database 0 can be migrated: a Valkey cluster has no
  other databases, and a later write to another database stops the migration.
- Target: an OpenEverest `valkey` instance (always a cluster). It must be
  empty, or enable *Delete all keys on the target*.
- **No pause, no resume of an interrupted run.** RedisShake cannot continue a
  stopped run: after a lost connection, a failover or a worker restart the
  worker empties the target and copies everything again (each time the source
  forks for a snapshot). After 5 failed copies in a row the migration fails;
  check the source's `client-output-buffer-limit replica` and `repl-timeout`
  and resume.
- Multi-key commands whose keys hash to different slots are split when the
  keys are independent (`MSET`, `MSETNX`, `DEL`, `UNLINK`); others (for
  example a cross-slot `RENAME` or `SUNIONSTORE`) stop the migration.
- Not copied: ACL users, configuration, Pub/Sub messages. Module data types
  (RedisJSON, Bloom, TimeSeries, …) cannot be migrated to Valkey. Hashes with
  field expiration can be migrated between servers of the same type (Redis 8
  → Redis 8, Valkey 9 → Valkey 9) only, until a fixed RedisShake is released.
- RedisShake encrypts its connections but does not verify server
  certificates; the worker's own checks do when a CA is provided.
- The worker stores the snapshot and the changes received during the copy on
  its local disk: plan ephemeral storage for the source's dataset size.

SQL Server (`mssql-log-shipping`):

- Source: SQL Server 2022+ (S3 backups need 2022), the databases in the FULL
  recovery model, and a `sysadmin` user (it backs up the databases, creates
  an S3 credential and reads `msdb` and the logins).
- Target: an OpenEverest `mssql` instance (provider-mssql publishes `sa`),
  the same or a newer version. The databases must not exist on the target, or
  enable *Recreate the target databases*.
- **Backup storage**: an S3 BackupStorage in the target's namespace. SQL
  Server reaches S3 over HTTPS only and always verifies the certificate: with
  a private CA, put it in the storage's credentials Secret as `ca.crt` and
  attach the storage to the OpenEverest instances (provider-mssql then
  mounts the CA); an external source must trust it itself. The plugin reads
  the storage's credentials with its ServiceAccount once you proved you can
  read the storage.
- **The migration owns the databases' log chain.** Any other log backup (an
  OpenEverest source's point-in-time recovery, a maintenance plan) takes log
  records the target never gets: the preflight refuses sources with such
  backups in the last 24 hours (enable *Other log backups are disabled* once
  you turned them off), and a later one fails the migration, which must be
  created again. The source keeps its log while the migration is paused.
- The **source SQL Server and the target SQL Server** both reach the storage,
  not only the worker.
- Availability groups: a source database's backups follow its primary. On a
  target availability group the databases are restored on the primary and,
  after the cutover, the operator adds them to the group and seeds the
  secondaries. If the target's primary moves before the cutover, the
  databases are copied again to the new primary.
- Logins are server objects: on an availability group source, a login that
  exists only on another replica than the primary is not copied (the
  migration's notes list such users).
- Not copied: Windows logins, server roles and permissions, SQL Agent jobs,
  linked servers and other server objects. Encrypted (TDE) databases need
  their certificate on the target first.
- Deleting a migration that did not complete leaves its databases restoring
  on the target: drop them or recover them (`RESTORE DATABASE … WITH
  RECOVERY`). With *Leave the source databases restoring*, bring a source
  database back with `RESTORE DATABASE … WITH RECOVERY` and `ALTER DATABASE
  … SET MULTI_USER`.

## Development

```bash
npm install
make frontend     # tsc + vite build (dist/main.js) + vitest
make backend      # gofmt + go vet + go test + go build
(cd workers/postgres-logical && go test ./...)   # PostgreSQL worker agent
(cd workers/mysql-replication && go test ./...)  # MySQL and MariaDB worker agent
(cd workers/redis-shake && go test ./...)        # Redis / Valkey worker agent
(cd workers/mssql-log-shipping && go test ./...) # SQL Server worker agent
make helm-lint
make dev-up       # k3d + Tilt (OpenEverest core + this plugin)
```

The backend embeds `backend/dist/main.js` (a placeholder in git; the Docker
build copies the real bundle) and serves `GET /main.js`, `GET /icon.svg`,
`GET /healthz` and `/api/...`.

## License

Apache-2.0
