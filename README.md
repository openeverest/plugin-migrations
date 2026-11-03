# OpenEverest Migrations

`plugin-migrations` moves databases **into** OpenEverest with minimal downtime,
much like AWS DMS. A per-migration worker copies existing data (**full load**),
keeps the target in sync with ongoing changes (**CDC**), and you trigger
**cutover** when your application is ready to switch.

> **Status: v0.1 draft.** The first engine is **MongoDB → MongoDB** built on
> [Percona ClusterSync for MongoDB](https://github.com/percona/percona-clustersync-mongodb)
> (PCSM). The plugin appears in the OpenEverest **sidebar** only.

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
4. **Delete**: removes the worker. Data on the target is kept.

The backend has no database. Migration records are ConfigMaps, so migrations
survive backend restarts and upgrades. Worker progress is checkpointed by the
engine itself (PCSM stores checkpoints in the target cluster).

## Engines

| Engine | Source | Target | Worker image |
| --- | --- | --- | --- |
| `pcsm` | MongoDB (OpenEverest `percona-server-mongodb` instance or external URI) | OpenEverest `percona-server-mongodb` instance | `percona/percona-clustersync-mongodb:1.0.0` |

PCSM options include namespace include/exclude filters (`db`, `db.*`,
`db.collection`), pausing once the initial sync completes, and clone/replication
worker tuning. See the options table in
[docs/architecture.md](docs/architecture.md) (§8).

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

## Development

```bash
npm install
make frontend     # tsc + vite build (dist/main.js) + vitest
make backend      # gofmt + go vet + go test + go build
make helm-lint
make dev-up       # k3d + Tilt (OpenEverest core + this plugin)
```

The backend embeds `backend/dist/main.js` (a placeholder in git; the Docker
build copies the real bundle) and serves `GET /main.js`, `GET /icon.svg`,
`GET /healthz` and `/api/...`.

## License

Apache-2.0
