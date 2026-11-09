# plugin-migrations — Architecture

> Status: **Draft v0.1** · Last updated: 2026-11-09

`plugin-migrations` is an OpenEverest generic plugin that moves data **into**
OpenEverest-managed databases with minimal downtime. It works like
[AWS DMS](https://aws.amazon.com/dms/): the user picks a source and a target, a
replication worker copies the existing data (full load), keeps the target in
sync with ongoing changes (CDC), and the user triggers a **cutover** once the
application is ready to switch.

The plugin is **modular**. Every database technology is an **engine** that
plugs into a shared lifecycle, storage, API and UI, the same way
plugin-bench adds benchmark drivers. The engines are **MongoDB → MongoDB**
built on
[Percona ClusterSync for MongoDB](https://github.com/percona/percona-clustersync-mongodb)
(PCSM), **PostgreSQL → PostgreSQL** built on native logical replication
(`postgres-logical`), **MySQL → MySQL** and **MariaDB → MariaDB** built on
native GTID replication (`mysql-replication`, `mariadb-replication`), and
**Redis / Valkey → Valkey** built on
[RedisShake](https://github.com/tair-opensource/RedisShake) (`redis-shake`).

## 1. Goals and non-goals (v0.1)

**Goals**

- A sidebar entry (**Migrations**) listing migrations, creating new ones, and
  showing a migration's progress and actions. The plugin has no cluster tab yet.
- Source: an **external URI** or an **OpenEverest instance**. Target: an
  **OpenEverest instance** of the same database type.
- Full load + CDC with explicit **pause / resume / cutover / delete**.
- Migrations survive plugin backend restarts and upgrades without an external
  database.
- New engines (for example PostgreSQL logical replication or MySQL) can be added
  without touching the lifecycle, storage or API layers.

**Non-goals (v0.1)**

- External targets and cross-engine migrations (for example MySQL → PostgreSQL).
- Schema conversion (DMS SCT equivalent).
- Data validation or row/document count comparison after cutover (see the
  roadmap).
- HA workers. PCSM supports HA, but v0.1 runs one worker replica.

## 2. Concepts

| DMS concept | plugin-migrations | Notes |
| --- | --- | --- |
| Source/target endpoint | **Endpoint** | `instance` (OpenEverest) or `external` (URI). Only sources can be external in v0.1. |
| Replication instance | **Worker** | One Kubernetes Deployment per migration, running the engine's image. |
| Migration task | **Migration** | The source, target, engine, options and lifecycle. |
| Task type | Engine options | PCSM always runs full load + CDC; `pauseOnInitialSync` stops when the initial sync completes. |

## 3. High-level architecture

```mermaid
flowchart LR
  UI[Plugin UI<br/>sidebar route] -->|api.fetch| Proxy[OpenEverest API<br/>plugin proxy]
  Proxy -->|Bearer JWT| BE[plugin-migrations backend]
  BE -->|user JWT: instances, connection| EV[OpenEverest API]
  BE -->|ServiceAccount| K8S[(Kubernetes API<br/>workload namespace)]
  K8S --- CM[ConfigMap<br/>migration record]
  CM -. owns .-> SEC[Secret<br/>endpoint credentials]
  CM -. owns .-> DEP[Deployment<br/>engine worker]
  CM -. owns .-> SVC[Service]
  BE -->|engine control API| SVC
  DEP -->|read + change stream| SRC[(Source DB)]
  DEP -->|write| TGT[(Target DB<br/>OpenEverest instance)]
```

Backend layers (Go, `backend/internal/...`):

| Package | Responsibility | Knows about |
| --- | --- | --- |
| `engine` | Technology-agnostic contracts: `Engine`, `Controller`, `WorkerSpec`, `Status`, `Phase`; engine registry. | Nothing else |
| `engine/pcsm` | MongoDB engine: option validation, worker spec (image, env, secrets), PCSM HTTP client, and mapping PCSM state to `engine.Status`. | `engine` |
| `engine/agentclient` | HTTP client for the agents in `workers/` (one API: status, start, pause, resume, cutover, teardown) and the mapping of agent states to `engine.Status`. | `engine` |
| `engine/postgreslogical` | PostgreSQL engine: option validation, worker spec (source/target URIs, the conninfo the target uses to subscribe), teardown. | `engine`, `engine/agentclient` |
| `engine/mysqlreplication` | MySQL and MariaDB engines (one worker image, `FLAVOR` env): option validation, worker spec (source/target URIs normalized to `ssl-mode`, CA files; provider-mariadb admin connections), teardown. | `engine`, `engine/agentclient` |
| `engine/redisshake` | Redis/Valkey engine: option validation, worker spec (redis[s]:// URIs, CA and provider-valkey client certificate files), teardown. Declares `CanPause: false`. | `engine`, `engine/agentclient` |
| `migration` | Domain service: create/list/get/act/delete, authorization, endpoint resolution, allowed actions, background reconciler. | `engine`, `Store`/`Workloads`/`Everest` interfaces |
| `kube` | ConfigMap-backed record store plus the worker objects (Secret/Deployment/Service) owned by the record. | Kubernetes, `migration` types |
| `everest` | OpenEverest API client using the caller's JWT (instances, access check, connection details). | HTTP |
| `api` | HTTP handlers, request decoding, error mapping. | `migration` |

The frontend (`src/`) mirrors the backend. A frontend **engine registry**
(`src/engines/`) provides each engine's label, compatible providers, default
options and an options form. The shared pages (list, create, details) do not
know about specific engines.

## 4. Engine contract

```go
// engine.Engine: pure, no I/O. Shared across requests.
type Engine interface {
    ID() string                                     // "pcsm"
    Describe() Descriptor                           // display name, source/target DB types, target providers, CanPause
    Supports(source, target EndpointInfo) bool      // database type compatibility
    Prepare(PrepareInput) (Prepared, error)         // validate options → WorkerSpec + safe options snapshot
    Controller(baseURL string) Controller           // client for a running worker
}

// engine.Controller: talks to a running worker.
type Controller interface {
    Start(ctx, options json.RawMessage) error
    Status(ctx) (Status, error)                     // normalized phase + progress
    Pause(ctx) error
    Resume(ctx) error                               // engine decides how to resume (e.g. PCSM fromFailure)
    Cutover(ctx) error                              // PCSM: /finalize
}

// engine.Teardowner: optional, implemented by controllers whose workers leave
// state behind (a PostgreSQL replication slot keeps WAL on the source).
type Teardowner interface {
    Teardown(ctx) error                             // remove it; data on the target stays
}
```

`Prepare` receives the resolved endpoint connections (URI, username, password,
CA) and trusted runner configuration (image and pull policy from Helm). It
returns:

- `WorkerSpec`: container port, health path, env, secret data, secret env and
  secret files (TLS CAs). Credentials appear only in the secret fields.
- `Snapshot`: effective options after defaults. It is shown in the UI and
  persisted, so it **must not** contain credentials.

Adding an engine takes three steps: implement `engine.Engine` in
`internal/engine/<id>`, register its constructor in `main.go`, and add a
`engines.<id>` block (enabled flag and image) to Helm. The frontend adds
`src/engines/<id>/`.

### Normalized phases

```mermaid
stateDiagram-v2
  [*] --> provisioning
  provisioning --> starting: worker ready
  starting --> full_load: start accepted
  full_load --> catching_up: clone done
  catching_up --> replicating: initial sync complete
  replicating --> paused: pause
  catching_up --> paused: pause
  paused --> catching_up: resume
  paused --> replicating: resume
  replicating --> finalizing: cutover
  paused --> finalizing: cutover (if initial sync complete)
  finalizing --> completed
  full_load --> failed
  catching_up --> failed
  replicating --> failed
  failed --> catching_up: resume
  completed --> [*]
```

`unknown` is reported when the worker cannot be reached after it was started.

PCSM mapping:

| PCSM `/status` | Phase |
| --- | --- |
| worker Deployment not available | `provisioning` |
| `state=idle` | `starting` |
| `running` and not `initialSync.cloneCompleted` | `full-load` |
| `running`, clone complete, not `initialSync.completed` | `catching-up` |
| `running` and `initialSync.completed` | `replicating` (ready for cutover) |
| `paused` | `paused` |
| `finalizing` | `finalizing` |
| `finalized` | `completed` |
| `failed` | `failed` |

`postgres-logical` mapping (the agent's `/status`):

| Agent state | Phase |
| --- | --- |
| `idle` | `starting` |
| `running`, not every table in subscription state `r` (ready) | `full-load` (schema copy, then table sync) |
| `running`, tables ready, heartbeat lag above 15s | `catching-up` |
| `running`, tables ready, lag ≤ 15s once | `replicating` (ready for cutover) |
| `paused` (subscription disabled) | `paused` |
| `finalizing` | `finalizing` |
| `completed` | `completed` |
| `failed` | `failed` |

Lag is the source's `now()` minus the last heartbeat replicated to the target,
so it uses only the source clock.

`mysql-replication` and `mariadb-replication` use the same agent states and
phases. `full-load` covers the preflight and the copy (MySQL Shell or
mydumper/myloader); once the channel runs, the phase is
`catching-up` until the replicated heartbeat lag is 15s or less (then
`replicating`). A channel stopped by an applier error stays `replicating` (or
`catching-up`) with the error shown; the agent restarts it every minute, so it
continues once the cause is fixed.

`redis-shake` uses the same agent states. `full-load` covers the preflight and
the snapshot RedisShake loads (progress is the snapshot bytes applied, summed
over the source shards); then `catching-up` until the heartbeat lag is 15s or
less. RedisShake cannot resume a run: when it stops on a lost connection or a
restart, the worker empties the target and copies again from the beginning,
and the phase goes back to `full-load` with the reason in the message.

### Allowed actions

The backend computes `allowedActions` so the UI does not duplicate engine rules.

| Action | Allowed when |
| --- | --- |
| `pause` | `catching-up`, `replicating`, for engines whose descriptor sets `canPause` (PCSM can only pause change replication; `redis-shake` cannot pause) |
| `resume` | `paused`, `failed` |
| `cutover` | `replicating`, or `paused` with the initial sync complete |
| `delete` | always (the UI asks for confirmation unless the migration is `completed`/`failed`) |

## 5. Storage: Kubernetes-native records

The backend keeps no state of its own. Every migration is one **ConfigMap** in
the workload namespace:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mig-7f3k2p9q
  labels:
    app.kubernetes.io/managed-by: plugin-migrations
    migrations.openeverest.io/id: mig-7f3k2p9q
    migrations.openeverest.io/engine: pcsm
data:
  migration.json: |   # Record (no credentials)
    { "id": "...", "name": "...", "engine": "pcsm", "source": {...}, "target": {...},
      "options": {...}, "createdAt": "...", "startedAt": "...", "cutoverRequestedAt": null }
```

The ConfigMap **owns** (through `ownerReferences`) the migration's Secret
(source/target URIs and CAs), Deployment (worker) and Service. Deleting the
record deletes the whole migration through Kubernetes garbage collection.
Updates use `resourceVersion` optimistic concurrency.

Live progress is not persisted. The reconciler polls each worker every few
seconds and caches the latest `engine.Status` in memory. After a restart the
cache rebuilds within one poll. PCSM keeps its own durable checkpoints in the
target cluster, so a worker restart resumes replication without the backend's
help.

`migration.Store` is an interface, so a Migration CRD and controller can replace
it later (for GitOps) without changing the domain service.

## 6. Lifecycle and the reconciler

1. **Create** (`POST /api/migrations`):
   1. Validate the request and select the engine (`registry.Resolve`).
   2. Resolve endpoints. OpenEverest instances use
      `GET /v1/clusters/{c}/namespaces/{ns}/instances/{name}/connection` with
      the **caller's JWT**, which also proves `read-connection` permission.
      External sources use the URI from the request.
   3. Reject a target that already has a migration record, whatever its phase,
      or a worker pod still shutting down (see §11, "One migration per
      target"). Users delete the previous migration first.
   4. `engine.Prepare`, then create the ConfigMap and the owned Secret,
      Deployment and Service. Engines can add a one-shot **bootstrap** Job;
      the Deployment then starts at zero replicas and the reconciler scales it
      to one after the Job succeeds, so worker restarts never re-run it. PCSM
      uses `pcsm reset` to clear checkpoints and HA state a previous migration
      left on the target; without it a new worker "recovers" the old,
      possibly finalized, migration instead of starting fresh.
2. **Reconcile** (background, every 5s): for each record, check that the
   Deployment is available and get the worker status. If the record was never
   started and the worker reports `idle`, call `Start(options)` and stamp
   `startedAt`. The migration therefore starts even when the browser is closed.
3. **Act** (`pause`/`resume`/`cutover`): check the caller's access, check the
   action is allowed for the live phase, then call the controller.
4. **Delete**: if the engine's controller is a `Teardowner` and the migration
   was started, call `Teardown` first (PostgreSQL: drop the subscription, the
   source slot and publication, and the heartbeat tables; MySQL: remove the
   replication channel and the `openeverest_migration` databases). A failed teardown
   returns 502 and keeps the record, so nothing is left retaining WAL
   silently; `?force=true` skips it when the source no longer exists. Then
   delete the ConfigMap. The worker stops and data already written to the
   target is kept.

## 7. Security

- The plugin never stores the user JWT. It uses the JWT only during a request.
- Endpoint credentials are copied once into the migration's Secret. They are
  never returned by the API or logged. External URIs are shown only as
  `host:port` lists with credentials removed.
- Authorization: listing and reading a migration requires read access to the
  **target** instance (and to the source instance when the source is managed),
  checked through the OpenEverest API with the caller's JWT. Create and actions
  require `read-connection` on the target (and on a managed source), which is
  a stronger permission than reading.
- The backend ServiceAccount is limited to ConfigMaps, Secrets, Services,
  Deployments, bootstrap Jobs and listing Pods in the workload namespace.
- Workers run as non-root with a restricted security context. A
  NetworkPolicy can restrict worker ingress to the plugin backend (roadmap).

## 8. HTTP API (v0.1 contract)

All routes are proxied by OpenEverest at
`/v1/clusters/{cluster}/plugins/plugin-migrations/...`. Errors return
`{"error": "message"}` with status 400 (validation), 401/403 (auth), 404,
409 (conflict, or an action not allowed in the current phase) or 502 (an
upstream or worker failure).

### `GET /api/status`

```json
{
  "status": "ready",
  "storage": "kubernetes",
  "engines": [
    {
      "id": "pcsm",
      "displayName": "Percona ClusterSync for MongoDB",
      "sourceDatabaseTypes": ["mongodb"],
      "targetDatabaseTypes": ["mongodb"],
      "targetProviders": ["percona-server-mongodb"],
      "canPause": true
    }
  ]
}
```

### `GET /api/instances?k8sCluster=main`

Accessible OpenEverest instances (metadata only), used for the source and target
pickers. The response matches plugin-bench:

```json
{ "instances": [ { "k8sCluster": "main", "namespace": "shop", "name": "orders", "provider": "percona-server-mongodb" } ] }
```

### `GET /api/migrations`

`{ "migrations": [Migration] }`, sorted newest first and filtered to the
migrations the caller may see.

### `POST /api/migrations`

```json
{
  "name": "orders-into-everest",
  "engine": "pcsm",
  "source": { "type": "external", "uri": "mongodb://user:pass@db1:27017,db2:27017/?replicaSet=rs0", "caCert": "" },
  "target": { "type": "instance", "k8sCluster": "main", "namespace": "shop", "name": "orders" },
  "options": { "includeNamespaces": ["shop.*"], "excludeNamespaces": [], "pauseOnInitialSync": false }
}
```

`source` can also be `{ "type": "instance", "k8sCluster", "namespace", "name" }`.
The response is `201` with a `Migration`.

### `GET /api/migrations/{id}`

Returns a `Migration` with a live status read from the worker.

### `POST /api/migrations/{id}/pause` · `/resume` · `/cutover`

`202` with the updated `Migration`. Returns `409` when the action is not
allowed in the current phase.

### `DELETE /api/migrations/{id}`

`204`. Cleans up the engine's replication state (see §6), then stops and
removes the worker. Data on the target is kept. `?force=true` skips the
cleanup; the UI offers it as **Delete anyway** after a failed delete.

### `Migration`

```json
{
  "id": "mig-7f3k2p9q",
  "name": "orders-into-everest",
  "engine": "pcsm",
  "source": { "type": "external", "display": "db1:27017,db2:27017" },
  "target": { "type": "instance", "k8sCluster": "main", "namespace": "shop", "name": "orders", "provider": "percona-server-mongodb" },
  "options": { "includeNamespaces": ["shop.*"], "excludeNamespaces": [], "pauseOnInitialSync": false, "clone": {}, "replication": {} },
  "createdAt": "2026-11-03T10:00:00Z",
  "startedAt": "2026-11-03T10:00:20Z",
  "phase": "full-load",
  "message": "Initial Sync: Cloning Data",
  "error": "",
  "progress": {
    "bytesTotal": 1073741824,
    "bytesCopied": 268435456,
    "lagSeconds": 0,
    "eventsApplied": 0,
    "lastReplicatedAt": "",
    "readyForCutover": false
  },
  "observedAt": "2026-11-03T10:05:00Z",
  "allowedActions": ["delete"]
}
```

Phases: `provisioning`, `starting`, `full-load`, `catching-up`, `replicating`,
`paused`, `finalizing`, `completed`, `failed`, `unknown`.
`startedAt`, `observedAt` and `progress.lastReplicatedAt` may be omitted when
unknown. `source.display` is set only for external sources.

### PCSM engine options (`engine: "pcsm"`)

| Field | Type | Default | PCSM `/start` field |
| --- | --- | --- | --- |
| `includeNamespaces` | `string[]` (`db.*` or `db.coll`; a bare `db` means `db.*`) | `[]` (all) | `includeNamespaces` |
| `excludeNamespaces` | `string[]` | `[]` | `excludeNamespaces` |
| `pauseOnInitialSync` | `bool` | `false` | `pauseOnInitialSync` |
| `clone.numParallelCollections` | `int` (1–64) | PCSM default | `cloneNumParallelCollections` |
| `clone.numReadWorkers` | `int` (1–256) | PCSM default | `cloneNumReadWorkers` |
| `clone.numInsertWorkers` | `int` (1–256) | PCSM default | `cloneNumInsertWorkers` |
| `replication.numWorkers` | `int` (1–256) | PCSM default | `replNumWorkers` |

### PostgreSQL engine options (`engine: "postgres-logical"`)

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `database` | `string` | database in the source URI | Required for instance sources. The target database gets the same name. |
| `includeSchemas` | `string[]` | `[]` (all) | Schemas to migrate. |
| `excludeSchemas` | `string[]` | `[]` | `openeverest_migration` is reserved. |
| `excludeTables` | `string[]` (`schema.table`) | `[]` | |
| `recreateDatabase` | `bool` | `false` | Drop and recreate the target database if it exists. Otherwise an existing database fails the migration. |

Only `provider-percona-postgresql` targets are offered, because `CREATE
SUBSCRIPTION` needs the superuser that its connection details provide. The
source and target must be different servers.

### MySQL engine options (`engine: "mysql-replication"`)

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `databases` | `string[]` | database in the source URI | Required for instance sources. Databases keep their names on the target. System databases and `openeverest_migration` cannot be migrated. |
| `excludeTables` | `string[]` (`database.table`) | `[]` | Neither copied nor replicated (`REPLICATE_IGNORE_TABLE`). Must belong to a migrated database. |
| `recreateDatabases` | `bool` | `false` | Drop the databases on the target first. Otherwise an existing database fails the preflight. |
| `copyThreads` | `int` (1–32) | `4` | MySQL Shell threads reading the source, and as many writing to the target. |

Views, routines, triggers and events whose definer holds `SYSTEM_USER` on
the target (for example `root@localhost`) are recreated with the target user
as definer, because that user lacks `SYSTEM_USER`. Each migration has its own
heartbeat table, so several migrations can share a source.

Only `percona-xtradb-cluster` targets are offered: their connection details
are `root`, which can configure replication, `gtid_purged` and `local_infile`,
and PXC runs with `skip_replica_start`, which keeps a restarted node from
replicating before the worker restores the channel filters (MySQL does not
persist them). The worker pins the channel to the PXC node behind the proxy
(`wsrep_node_incoming_address`), because the copy's `gtid_purged` exists only
on that node. See [workers/mysql-replication](../workers/mysql-replication/README.md).

Source URIs may set `ssl-mode` (MySQL client names) or `tls` (Go driver
names, as OpenEverest providers publish: `tls=false` is `DISABLED`). A CA
certificate makes the worker verify the server (`VERIFY_CA`); verifying modes
without a CA are rejected. The target server's replication connection uses TLS
unless the source is `DISABLED`, but cannot verify it.

### MariaDB engine options (`engine: "mariadb-replication"`)

The options are those of `mysql-replication`; `copyThreads` sets mydumper's
and myloader's threads. The same worker runs with `FLAVOR=mariadb`:

- mydumper streams a consistent snapshot (`BACKUP STAGE BLOCK_COMMIT`) into
  myloader through the worker, which reads the snapshot's GTID position from
  the stream's `metadata` file. Nothing is written to the worker's disk except
  the tools' option files (credentials never appear in a command line).
- The target sets `gtid_slave_pos` and replicates over a named connection
  (`CHANGE MASTER 'mig_…'`, `MASTER_USE_GTID=slave_pos`) with the same table
  filters, set through the connection's `replicate_*` variables. MariaDB
  has no `SYSTEM_USER`, so definers are kept.
- OpenEverest instances are reached with the admin (root) connection that
  provider-mariadb 0.2.2+ publishes as `admin-*` keys, which the backend
  reads from the instance's connection details (`engine.Connection.Properties`).
  The application user cannot replicate. Only targets whose provider
  publishes `admin-skip-replica-start=true` (standalone and Galera: started
  with `skip-slave-start`) are accepted. MariaDB does not expose that setting
  as a variable, so the worker cannot check it itself. A replication-topology
  target is refused: the operator does not start a replica's server again
  after its own replication changes, and a switchover resets
  `gtid_slave_pos`.
- On Galera, `gtid_slave_pos` is per node, so the channel is pinned to the
  node behind the Service: the entry of `wsrep_cluster_address` named after
  `wsrep_node_name` (mariadb-operator leaves `wsrep_node_incoming_address`
  at `AUTO`).
- Versions: source 10.6+, target 10.11+ and the same or a newer series.
  Preflight also requires `binlog_format=ROW` and different `server_id`s on
  the source and the target (both engines check this: a replica ignores
  changes carrying its own `server_id`).

### Redis / Valkey engine options (`engine: "redis-shake"`)

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `flushTarget` | `bool` | `false` | Delete every key on the target before the copy. Otherwise a target with keys fails the preflight. |
| `excludeKeyPrefixes` | `string[]` (≤ 64) | `[]` | Keys starting with these are neither copied nor replicated. Applied in RedisShake's Lua hook, not `block_key_prefix`, which silently drops a whole command when only some of its keys match; a command that changes both excluded and migrated keys (other than `MSET`/`MSETNX`/`DEL`/`UNLINK`, which are split) stops the migration. `__openeverest_migration:` is reserved. |

Sources are external `redis[s]://` / `valkey[s]://` URIs (a standalone
primary or any primary of a cluster) or `valkey` instances. Targets are
`valkey` instances, always clusters (provider-valkey runs Valkey in cluster
mode, also for its `replication` topology). The worker:

- **Preflight**: the source is a primary; both clusters report `cluster_state:ok`;
  the source has keys only in database 0; no hashes with field expiration
  between different server types (Redis `subexpiry`, Valkey
  `keys_with_volatile_items`); the target is empty unless `flushTarget`; the
  source and target `run_id`s differ.
- **Copy and replication**: RedisShake 4.7 (`sync_reader` → `redis_writer`,
  cluster mode as detected) with `rdb_restore_command_behavior = "rewrite"`.
  A Lua hook stops the migration on writes to another database and drops
  `PUBLISH`.
- **Heartbeat**: one key for every pair of a source shard and a target shard
  that share slots (`__openeverest_migration:{tag}:<name>:heartbeat`, the tag
  picking a shared slot), set to the source clock every 5s. RedisShake sends
  one source shard's changes for one target shard over one ordered
  connection, so a heartbeat on the target proves everything sent before it
  in that pair was applied. Lag is the age of the oldest heartbeat not yet on
  the target.
- **State**: `__openeverest_migration:<name>:state` on the target (`copying`,
  `failed` with the reason, `stopped`, `completed`). A restarted worker copies
  again only from `copying`; `failed` waits for a resume, and `stopped` (the
  cutover had stopped RedisShake) only finishes the cleanup. Only once the
  state exists does the worker empty the target on its own; a migration that
  failed its preflight runs it again on resume.
- **Cutover**: writes a final heartbeat, newer than every earlier one, into
  every shard pair and waits until the target has all of them (at most 15
  minutes), so every change made before the request is applied even while
  the source keeps taking writes. It then stops RedisShake and removes the
  heartbeat keys on both sides.

## 9. UI (sidebar only in v0.1)

- **Migrations list** at `/plugins/plugin-migrations`: a table with name,
  engine, source → target, phase chip, progress (bytes copied / total during the
  full load, lag during CDC) and created time. It refreshes every 5s and has a
  **Create migration** button.
- **Create migration** is a stepper:
  1. **Engine**: the cards come from `/api/status` and the frontend engine
     registry.
  2. **Source**: choose *OpenEverest instance* (picker filtered to the engine's
     providers) or *External* (URI and an optional CA).
  3. **Target**: an OpenEverest instance (excluding the source).
  4. **Options**: the engine's options form (namespace filters, pause on initial
     sync, an *Advanced* section with tuning).
  5. **Review**, then create.
- **Migration details**: a phase stepper (Provisioning → Full load → CDC →
  Cutover → Completed), progress bar, lag, events applied and last replicated
  time. Actions follow `allowedActions`. **Cutover** opens a checklist dialog
  (stop writes on the source, wait for lag ≈ 0, confirm). The page polls every
  3s.

## 10. Helm

- `engines.pcsm.enabled` and `engines.pcsm.image`
  (`percona/percona-clustersync-mongodb:1.0.0`).
- `engines.postgres-logical.enabled` and `engines.postgres-logical.image`
  (`ghcr.io/openeverest/plugin-migrations-postgres-logical`, released with
  the plugin).
- `engines.mysql-replication.enabled` and `engines.mysql-replication.image`
  (`ghcr.io/openeverest/plugin-migrations-mysql-replication`, released with
  the plugin).
- `engines.mariadb-replication.enabled` and `engines.mariadb-replication.image`
  (the same image as `mysql-replication`).
- `engines.redis-shake.enabled` and `engines.redis-shake.image`
  (`ghcr.io/openeverest/plugin-migrations-redis-shake`, with RedisShake 4.7.0,
  released with the plugin).
- `workload.namespace` (defaults to the release namespace) holds the records and
  workers. The chart includes a Role and RoleBinding for that namespace.
- `worker.resources`, `worker.nodeSelector` and `worker.tolerations` apply to
  all workers.
- Plugin CR extension points: `route` and `sidebarItem` only. Permissions:
  `read namespaces`, `read instances`, `read-connection instances`.

## 11. Open questions and risks

1. **Target privileges.** The PSMDB connection secret uses `databaseAdmin`
   (`readWriteAnyDatabase`, `dbAdminAnyDatabase`, `backup`, `restore`,
   `clusterMonitor`). PCSM on a **sharded** target may also need
   `clusterManager`. Validate this, and either add a preflight check or ask the
   provider for a dedicated migration user.
2. **PCSM state in the target.** PCSM writes its checkpoints and HA membership
   to the target. Deleting a migration does not clean them up; the next
   migration to the same target clears them in its bootstrap (`pcsm reset`).
3. **Network reachability.** External sources must be reachable from the worker
   pod. Add a connectivity preflight (a short-lived Job running `ping`) before
   the worker is created.
4. **RBAC for actions.** OpenEverest has no plugin-facing "can update instance"
   check. v0.1 uses `read-connection` as the gate for mutations.
5. **One migration per target.** PCSM keeps a single lease and checkpoint per
   target, and `pcsm reset` is only safe while no worker runs against it. A
   completed migration's worker keeps running until its record is deleted, so
   the backend rejects a new migration while the target has any migration
   record or a worker pod that is still shutting down.
6. **PostgreSQL WAL retention.** The source slot retains WAL while a migration
   runs or is paused. Delete tears it down; a forced delete leaves it for the
   user. A status warning or a `max_slot_wal_keep_size` check would help.
7. **PostgreSQL reachability.** The subscription connects from the **target
   instance's** pods, not from the worker. A worker-only connectivity check
   does not prove the target can reach the source.
8. **PostgreSQL schema changes.** DDL is not replicated; the user must freeze
   it until cutover. Operator-managed schemas (`pgbouncer`) differ between
   providers and are excluded by a fixed list.
9. **MySQL binary log retention.** Unlike a PostgreSQL slot, a replication
   channel does not make the source keep its binary logs. If the copy or a
   pause outlasts the source's retention, the channel fails with error 1236
   and the migration must be created again. A preflight warning based on
   `binlog_expire_logs_seconds` (RDS: binlog retention hours) would help.
10. **MySQL channel placement on PXC.** The channel and the copy's
    `gtid_purged` live on one node. A restarted node keeps them and the worker
    restarts the channel; a node rebuilt by SST loses them, and the migration
    fails. Appending `gtid_purged` on every node, or the operator's
    `replicationChannels` (which also moves the channel when the primary
    changes), would let the channel follow the primary; the latter needs
    provider support.
11. **MySQL filters and non-table DDL.** Table filters do not match statements
    that touch no table (`CREATE PROCEDURE other.p`). The worker skips such a
    transaction when it fails with *Unknown database* for a database that is
    not migrated; any other failure stops the channel and is reported.
    Database filters (`REPLICATE_DO_DB`) are not used: they match DDL by the
    session's default database and would skip qualified DDL silently.
12. **MySQL targets other than PXC.** provider-percona-server-mysql publishes
    no connection URI, which the backend requires, and Group Replication
    primaries change. Supporting it needs a URI from the provider and a plan
    for primary changes.
13. **MariaDB targets.** The same binary log retention and channel placement
    limits apply (a Galera node rebuilt by SST loses `gtid_slave_pos`). On
    Galera, account statements (`CREATE/ALTER/DROP USER`, roles, `GRANT`,
    `REVOKE`) from the source reach the other nodes: `mysql_execute_command`
    begins total order isolation before the ACL code checks the replication
    filters, so the replica node skips them while the rest of the cluster
    executes them (verified on MariaDB 12.3; PXC filters them first). The
    worker cannot prevent this; the UI warns users not to change accounts on
    the source during a Galera migration. A fix belongs upstream.
    Replication-topology targets need the operator to keep a migration
    connection across restarts and switchovers. Grants through roles are not
    seen by the source privilege check (`SHOW GRANTS` of the user only).
14. **Redis/Valkey: no resume.** RedisShake 4 restarts from a full snapshot
    after any interruption, and each attempt makes every source shard fork for
    a snapshot; the worker gives up after 5 failed copies in a row. Resuming
    is possible in principle (`PSYNC <replid> <offset>` continues, also across
    a source failover), but applying replayed commands exactly once needs a
    per-slot checkpoint written in the same transaction as the data, which
    RedisShake does not do (cluster targets reject cross-slot transactions).
15. **Redis/Valkey: stock RedisShake 4.7 limits.** It does not verify server
    certificates (the worker's own checks do), and it cannot copy hashes with
    field expiration between Redis 8 and Valkey 9, so the preflight refuses
    such sources. Both are fixed upstream in tair-opensource/RedisShake#1075
    and #1074 (pending). Module types (RedisJSON, Bloom, …) cannot be copied
    to Valkey. Commands whose keys hash to different slots are split when the
    keys are independent (`MSET`, `MSETNX`, `DEL`, `UNLINK`); others stop the
    migration with a message.

## 12. Roadmap

- v0.2: preflight checks (connectivity, versions, privileges, oplog window),
  worker logs in the UI, Prometheus metrics passthrough (PCSM `/metrics`), and
  `clusterDetailTab` on target instances.
- v0.3: HA workers (2 replicas, PCSM lease), post-cutover validation (counts and
  sample hashes), PCSM state cleanup on delete.
- PostgreSQL: a physical-standby engine (`pg_basebackup` + streaming
  replication, promote at cutover) for whole-cluster migrations with DDL. It
  needs provider support to create an instance as a standby and promote it.
  `pgcopydb` was evaluated and set aside until its CDC fixes are released.
- MySQL: a physical engine (restore an XtraBackup backup into the target,
  then attach the replication channel) for large databases, which needs
  provider support; Percona Server (Group Replication) targets; MariaDB
  replication-topology targets.
- Redis/Valkey: resumable runs (per-slot checkpoints, upstream in RedisShake
  or in the worker), and the RedisShake fixes for TLS verification and field
  expiration once released.
- Cross-engine migrations (for example with Debezium Server and its JDBC sink).
- Optional Migration CRD and controller for GitOps.
