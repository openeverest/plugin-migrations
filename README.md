# OpenEverest Migrations

`plugin-migrations` moves databases **into** OpenEverest with minimal downtime,
much like AWS DMS. A per-migration worker copies existing data (**full load**),
keeps the target in sync with ongoing changes (**CDC**), and you trigger
**cutover** when your application is ready to switch.

> **Status: v0.1 draft.** Engines: **MongoDB → MongoDB** built on
> [Percona ClusterSync for MongoDB](https://github.com/percona/percona-clustersync-mongodb)
> (PCSM), and an **experimental PostgreSQL → PostgreSQL** engine built on
> [pgcopydb](https://github.com/dimitri/pgcopydb), off by default. The plugin
> appears in the OpenEverest **sidebar** only.

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
   **Cutover**. PCSM finalizes the target (indexes and other catalog work).
4. **Delete**: removes the worker. Data on the target is kept. Engines that
   leave state on the source or target (the pgcopydb replication slot and
   origin) clean it up first; if that fails the deletion is refused, and
   `DELETE /api/migrations/{id}?force=true` skips the cleanup.

The backend has no database. Migration records are ConfigMaps, so migrations
survive backend restarts and upgrades. Worker progress is checkpointed by the
engine itself (PCSM stores checkpoints in the target cluster).

## Engines

| Engine | Source | Target | Worker image |
| --- | --- | --- | --- |
| `pcsm` | MongoDB (OpenEverest `percona-server-mongodb` instance or external URI) | OpenEverest `percona-server-mongodb` instance | `percona/percona-clustersync-mongodb:1.0.0` |
| `pgcopydb` (experimental) | One PostgreSQL database (OpenEverest `provider-percona-postgresql` instance or external URI) | OpenEverest `provider-percona-postgresql` instance | `ghcr.io/openeverest/plugin-migrations-pgcopydb` ([workers/pgcopydb](workers/pgcopydb)) |

PCSM options include namespace include/exclude filters (`db`, `db.*`,
`db.collection`), pausing once the initial sync completes, and clone/replication
worker tuning. pgcopydb options select the database, include/exclude schemas,
exclude tables, replace existing objects on the target, and copy/index job
counts. See the options tables in [docs/architecture.md](docs/architecture.md)
(§8).

> **pgcopydb engine status.** pgcopydb v0.18, the latest release, stalls and
> has open data-loss issues in change replication; the fixes are merged on
> upstream main for the next release (v0.19). The engine is disabled by default
> and its worker image is built from v0.18; build it from upstream main
> (`--build-arg PGCOPYDB_IMAGE=ghcr.io/dimitri/pgcopydb:latest`) to evaluate,
> and switch to v0.19 once it is released.

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
| `engines.pgcopydb.enabled` | Enable the experimental PostgreSQL (pgcopydb) engine | `false` |
| `engines.pgcopydb.image.*` | pgcopydb worker image (released with the plugin) | `ghcr.io/openeverest/plugin-migrations-pgcopydb:<chart version>` |
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
- pgcopydb migrates one database per migration; it keeps its name on the
  target, where it is created if missing. The source needs `wal_level=logical`
  and a user with `REPLICATION` and read access; the worker keeps a heartbeat
  table in a `pgcopydb` schema on the source (never migrated). Targets must
  publish a superuser connection (`provider-percona-postgresql` does; disable
  its PgBouncer or use session pooling). Object owners and grants are not
  copied (`--no-owner --no-acl`). A worker restart before the cutover loses the
  local pgcopydb state: the migration then fails and must be recreated.

## Development

```bash
npm install
make frontend     # tsc + vite build (dist/main.js) + vitest
make backend      # gofmt + go vet + go test + go build
make helm-lint
(cd workers/pgcopydb && go test ./...)   # pgcopydb worker agent
make dev-up       # k3d + Tilt (OpenEverest core + this plugin)
```

The backend embeds `backend/dist/main.js` (a placeholder in git; the Docker
build copies the real bundle) and serves `GET /main.js`, `GET /icon.svg`,
`GET /healthz` and `/api/...`.

## License

Apache-2.0
