# OpenEverest Migrations

`plugin-migrations` moves databases **into** OpenEverest with minimal downtime,
much like AWS DMS. A per-migration worker copies existing data (**full load**),
keeps the target in sync with ongoing changes (**CDC**), and you trigger
**cutover** when your application is ready to switch.

> **Status: v0.1 draft.** Engines: **MongoDB → MongoDB** built on
> [Percona ClusterSync for MongoDB](https://github.com/percona/percona-clustersync-mongodb)
> (PCSM), and **PostgreSQL → PostgreSQL** built on native logical replication.
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
   replication slot and publication from the source).
4. **Delete**: removes the worker. Data on the target is kept. Engines that
   leave replication state behind (PostgreSQL) clean it up first.

The backend has no database. Migration records are ConfigMaps, so migrations
survive backend restarts and upgrades. Worker progress is checkpointed by the
engine itself (PCSM stores checkpoints in the target cluster; PostgreSQL keeps
the replication state in the target's subscription and the source's slot).

## Engines

| Engine | Source | Target | Worker image |
| --- | --- | --- | --- |
| `pcsm` | MongoDB (OpenEverest `percona-server-mongodb` instance or external URI) | OpenEverest `percona-server-mongodb` instance | `percona/percona-clustersync-mongodb:1.0.0` |
| `postgres-logical` | PostgreSQL 13+ (OpenEverest instance or external URI) | OpenEverest `percona-postgresql` instance (PostgreSQL 15+) | `ghcr.io/openeverest/plugin-migrations-postgres-logical` |

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

### Adding an engine

1. Backend: implement `engine.Engine` in `backend/internal/engine/<id>/`
   (`Prepare` builds the worker spec; `Controller` drives the running worker
   and maps its state onto the shared phases). Register its constructor in
   `compiledEngines` in `backend/main.go`.
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

## Development

```bash
npm install
make frontend     # tsc + vite build (dist/main.js) + vitest
make backend      # gofmt + go vet + go test + go build
(cd workers/postgres-logical && go test ./...)   # PostgreSQL worker agent
make helm-lint
make dev-up       # k3d + Tilt (OpenEverest core + this plugin)
```

The backend embeds `backend/dist/main.js` (a placeholder in git; the Docker
build copies the real bundle) and serves `GET /main.js`, `GET /icon.svg`,
`GET /healthz` and `/api/...`.

## License

Apache-2.0
