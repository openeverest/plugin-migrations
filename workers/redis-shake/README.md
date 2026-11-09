# redis-shake worker

The worker image for the `redis-shake` engine. It migrates Redis or Valkey
data, standalone or cluster, into an OpenEverest Valkey instance with
[RedisShake](https://github.com/tair-opensource/RedisShake) 4.7.0. RedisShake
connects to the source as a replica (`PSYNC`): it loads a snapshot of every
shard into the target, then applies the changes the source streams until the
cutover.

The image is distroless with the agent (`redis-shake-agent`) and the
unmodified RedisShake release binary (pinned by checksum). It runs as
65532:65532 and listens on `:2243`; RedisShake's status endpoint listens on
`:2244`.

## Flow

1. **Preflight**. The source is a primary (the agent writes heartbeat keys to
   it) and has keys only in database 0; clusters report `cluster_state:ok`;
   hashes with field expiration are only migrated between servers of the same
   type (RedisShake 4.7 cannot restore them between Redis 8 and Valkey 9); the
   target is empty unless `flushTarget`; source and target are different
   servers. Every problem is reported at once.
2. **State**. `__openeverest_migration:<name>:state` on the target records the
   phase (`copying`, `failed` with the reason, `stopped`, `completed`) and the
   options. From then on the target holds only this migration's data, and the
   agent may empty it (`FLUSHALL` on every primary) before a new copy. A
   restarted worker copies again only from `copying`: `failed` waits for a
   resume, and `stopped` (RedisShake was stopped at the cutover) only finishes
   the cleanup.
3. **Heartbeat**. One key for every pair of a source shard and a target shard
   that share slots, `__openeverest_migration:{tag}:<name>:heartbeat` (the
   hash tag picks a shared slot), set to the source clock every 5s.
   RedisShake sends one source shard's changes for one target shard over one
   ordered connection, so a heartbeat that reached the target proves
   everything sent before it in that pair was applied. The lag is the age of
   the oldest heartbeat not yet on the target.
4. **Copy and replication**. The agent writes a RedisShake configuration
   (mode 0600: it contains the passwords) and runs RedisShake: `sync_reader`
   (cluster mode if the source is a cluster) → `redis_writer` (cluster mode),
   `rdb_restore_command_behavior = "rewrite"`. A Lua hook:
   - splits `MSET`, `MSETNX`, `DEL` and `UNLINK` whose keys hash to different
     slots, which a cluster target rejects;
   - drops keys with an excluded prefix (RedisShake's own `block_key_prefix`
     silently drops a whole multi-key command when only some keys match);
   - stops the migration on any other cross-slot command, on a command that
     mixes excluded and migrated keys, and on writes to a database other
     than 0;
   - drops `PUBLISH`.
5. **Supervision**. RedisShake exits on any error and cannot resume. A
   transient failure (a lost connection, a failover, `LOADING`, …) makes the
   agent wait (5s, doubling up to 2 minutes), empty the target and copy
   again; after 5 failed copies in a row without finishing one, the migration
   fails. Other failures (a command the target rejects, a module type, …)
   fail the migration with RedisShake's error, with command arguments removed.
   Resume starts a new copy.
6. **Cutover**. The agent writes a final heartbeat, newer than every earlier
   one, into every shard pair and waits (at most 15 minutes) until the target
   has all of them: every change made on the source before the request is
   then applied, even if the source keeps taking writes. It then stops
   RedisShake, records `stopped`, removes the heartbeat keys on both sides and
   records the completion. A failed cutover stops RedisShake and fails the
   migration.

## HTTP API

| Route | Description |
| --- | --- |
| `GET /healthz` | Liveness and readiness. |
| `GET /status` | State, message, error, snapshot bytes applied/total, lag and `initialSyncCompleted`. |
| `POST /start` | Starts the migration with the JSON options below. Runs in the background. |
| `POST /pause` | Always 409: RedisShake cannot continue a stopped run. |
| `POST /resume` | Starts a failed migration again with a fresh copy (or, if it failed its preflight, the preflight). |
| `POST /cutover` | Runs the cutover in the background. |
| `POST /teardown` | Stops RedisShake and removes the heartbeat keys on both sides and the state key on the target. Data on the target stays. Idempotent. |

Start options: `flushTarget` (boolean) and `excludeKeyPrefixes` (up to 64
prefixes; `__openeverest_migration:` is reserved).

## Environment

| Variable | Description |
| --- | --- |
| `SOURCE_URI` | `redis[s]://user:password@host:port/?ca=…&cert=…&key=…` of the source (any primary of a cluster). `rediss` enables TLS; `ca` makes the agent verify the server, `cert`/`key` send a client certificate. |
| `TARGET_URI` | The target, same format. |
| `MIGRATION_NAME` | Names the heartbeat and state keys (`[a-z0-9_-]`, at most 63 characters). |
| `HEARTBEAT_INTERVAL` | Heartbeat period (default `5s`). |
| `WORK_DIR`, `SHAKE_BINARY` | Work directory (default `/work`) and RedisShake binary. |

RedisShake 4.7 encrypts but never verifies server certificates, and uses a
client certificate only together with a CA; the agent's own connections
verify the server against the URI's host when a CA is given.

## Limitations

- An interrupted run starts again from the beginning (see Supervision). Each
  attempt makes every source shard fork for a snapshot; the source needs
  `client-output-buffer-limit replica` and `repl-timeout` large enough for a
  full copy.
- The worker keeps each shard's snapshot and the changes received while
  loading it on its local disk.
- ACL users, configuration and Pub/Sub messages are not copied. Module data
  types cannot be restored on Valkey.
- A source cluster that changes its slots (resharding, failover) stops
  RedisShake; the agent copies again.

## Development

```bash
go test ./...
docker build -t plugin-migrations-redis-shake:dev .
```
