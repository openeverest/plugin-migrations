# pgcopydb worker

Worker image for the experimental `pgcopydb` engine: [pgcopydb](https://github.com/dimitri/pgcopydb)
plus `pgcopydb-agent`, a small HTTP server the plugin backend drives. One worker
runs per migration and migrates one database.

## Image

- Base: `dimitri/pgcopydb:v0.18` (`PGCOPYDB_IMAGE` build arg). Change
  replication in v0.18 stalls and has open data-loss issues; upstream main
  fixes them for v0.19. To evaluate before that release:
  `docker build --build-arg PGCOPYDB_IMAGE=ghcr.io/dimitri/pgcopydb:latest .`
- Adds the PostgreSQL 18 client tools (`PG_CLIENT_MAJOR`): the upstream image
  ships version 16, and `pg_dump` cannot dump a newer server.
- Runs as uid 999 (numeric, for `runAsNonRoot`); state lives in `/work`.

## Environment

| Variable | Description |
| --- | --- |
| `PGCOPYDB_SOURCE_PGURI` | Source URI, including the database to migrate |
| `PGCOPYDB_TARGET_PGURI` | Target URI for the same database name |
| `TARGET_ADMIN_PGURI` | Target URI for a maintenance database (creates the target database) |
| `TARGET_DATABASE` | Name of the target database |
| `SLOT_NAME` | Replication slot and origin name, unique per migration |
| `PORT`, `WORK_DIR`, `HEARTBEAT_INTERVAL` | `2242`, `/work`, `5s` |

## API

| Request | Effect |
| --- | --- |
| `GET /healthz` | Liveness and readiness |
| `GET /status` | `state` (`idle`, `running`, `paused`, `finalizing`, `completed`, `failed`, `stopped`), `copyDone`, `initialSyncCompleted`, bytes and `lagSeconds` |
| `POST /start` `{"options": {...}}` | Creates the target database and the source heartbeat, writes the filters, starts `pgcopydb clone --follow` |
| `POST /pause`, `/resume` | Toggles pgcopydb's apply mode; resume after a failure that happened after the copy runs `pgcopydb follow --resume` |
| `POST /cutover` | Sets the end position to the source's current WAL flush position |
| `POST /teardown` | Stops pgcopydb, drops the source slot, the target origin and the heartbeat row |

Errors are `{"error": "..."}` with `400` (bad options), `409` (not allowed in
the current state) or `500`.

## Implementation notes

- State is read from pgcopydb's SQLite catalog (`/work/run/schema/source.db`)
  with `sqlite3`, and the sentinel's `apply` and `endpos` are written there
  directly. The pgcopydb CLI is avoided: in v0.18 `list progress` rewrites the
  stored filters of a running clone, `sentinel get --json` misreports
  `endpos`, and `sentinel set` refuses catalogs created with `--filters`.
- pgcopydb ignores changes to tables in the `pgcopydb` schema. The agent
  updates `pgcopydb.heartbeat` on the source every few seconds: the age of the
  oldest heartbeat not yet replayed is the lag, and after a cutover the
  heartbeats move the source past the end position so pgcopydb finishes on an
  idle source. Logical decoding messages are never emitted: pgcopydb versions
  before the fix for upstream issue 1060 stop applying on foreign messages.
- A worker restart loses `/work`. At startup the agent fails the migration if
  its slot already exists, instead of starting over and skipping changes.
