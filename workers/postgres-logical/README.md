# postgres-logical worker

The worker image for the `postgres-logical` engine. It migrates one PostgreSQL
database into an OpenEverest instance with **native logical replication**. The
worker does not move rows itself: it prepares both sides, creates a
publication on the source and a subscription on the target, and then the
target server copies the tables and streams changes directly from the source.

The image is `postgres:18-alpine` (for `pg_dump`, `pg_restore` and `psql`) plus
a small Go agent with no dependencies outside the standard library. It runs as
the `postgres` user (70:70) and listens on `:2242`.

## Flow

1. **Preflight**. Source: PostgreSQL 13+, `wal_level = logical`, a user with
   `REPLICATION` (superuser or `rds_replication` also work), at least one
   table to migrate, and every migrated table has a primary key or a replica
   identity. Target: PostgreSQL 15+, a superuser, and the same or a newer
   major version than the source.
2. **Target database**. Created if missing. If it exists the migration fails,
   unless `recreateDatabase` is set (`DROP DATABASE ... WITH (FORCE)`, then
   `CREATE DATABASE`).
3. **Heartbeat**. A `openeverest_migration.heartbeat` table on both sides. The
   agent updates the source row every 5s. Its replicated value on the target
   gives the lag, measured with the source clock only.
4. **Schema**. `pg_dump --schema-only` from the source, `pg_restore
   --no-owner --no-acl --exit-on-error` into the target. Schema filters apply.
   `openeverest_migration` and operator-managed schemas (`pgbouncer`, which the
   Percona operator creates in every database) are skipped.
5. **Replication**. `CREATE PUBLICATION mig_<id> FOR TABLE ...` (with
   `publish_via_partition_root`) on the source and `CREATE SUBSCRIPTION
   mig_<id> ... WITH (copy_data, streaming = on, disable_on_error = false)` on
   the target. The subscription creates the slot `mig_<id>` on the source.
   Apply errors are retried by PostgreSQL; the status shows a warning while
   errors keep occurring.
6. **Cutover**. The agent writes a token into the source heartbeat and waits
   until the target has applied it, so every change committed before the
   cutover is on the target. It then drops the subscription, sets every
   sequence to the source value, refreshes materialized views, and removes the
   slot, publication and heartbeat from the source. A completion marker on the
   target lets a restarted worker report `completed`.

The agent keeps no state of its own. After a restart it reads the subscription
and heartbeat tables to recover the phase, and replication never stops because
it runs between the two servers.

## HTTP API

| Route | Description |
| --- | --- |
| `GET /healthz` | Liveness and readiness. |
| `GET /status` | Phase, message, error, bytes copied/total, lag and `initialSyncCompleted`. |
| `POST /start` | Starts the migration with the JSON options below. Runs in the background. |
| `POST /pause` · `/resume` | `ALTER SUBSCRIPTION ... DISABLE` / `ENABLE`. The source slot keeps WAL while paused. |
| `POST /cutover` | Runs the cutover in the background. |
| `POST /teardown` | Removes the subscription, the target's `openeverest_migration` schema, and the slot, publication and heartbeat on the source. Data on the target stays. Idempotent. |

Start options: `database`, `includeSchemas`, `excludeSchemas`,
`excludeTables` (`schema.table`) and `recreateDatabase`.

## Environment

| Variable | Description |
| --- | --- |
| `SOURCE_PGURI` | Source URI used by the agent (`pg_dump`, preflight, cleanup). |
| `SUBSCRIPTION_CONNINFO` | Source URI the **target server** uses for the subscription. It has no local CA path; `verify-*` modes become `require`. |
| `TARGET_PGURI` | Target URI for the migrated database. |
| `TARGET_ADMIN_PGURI` | Target URI for the `postgres` database (create/drop database). |
| `TARGET_DATABASE` | Database name on the target. |
| `REPLICATION_NAME` | Name of the publication, subscription and slot (`mig_<id>`). |
| `HEARTBEAT_INTERVAL` | Heartbeat period (default `5s`). |

## Limitations

- DDL is not replicated. Freeze schema changes until cutover.
- Large objects are not copied.
- The target server must reach the source over the network.
- Tables without a primary key need `REPLICA IDENTITY FULL` (or an index
  identity) on the source, or must be excluded.

## Development

```bash
go test ./...
docker build -t plugin-migrations-postgres-logical:dev .
```
