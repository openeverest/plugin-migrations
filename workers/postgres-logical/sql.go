package main

// SQL run by the agent. Values arrive as psql variables: :'name' for
// literals, :"name" for identifiers, and lists as JSON arrays. Statements
// built from catalog names use format('%I') and \gexec.

// candidateTablesCTE selects the tables to migrate on the source: permanent
// ordinary and partitioned tables (partitions travel with their root),
// minus system, extension-owned, filtered and migration tables.
const candidateTablesCTE = `
with params as (
  select :'include_schemas'::jsonb as inc, :'exclude_schemas'::jsonb as exc, :'exclude_tables'::jsonb as exct
),
candidates as (
  select c.oid, c.relkind, n.nspname, c.relname
  from pg_class c
  join pg_namespace n on n.oid = c.relnamespace
  cross join params p
  where c.relkind in ('r', 'p') and c.relpersistence = 'p' and not c.relispartition
    and n.nspname not in ('pg_catalog', 'information_schema') and n.nspname not like 'pg\_%'
    and n.nspname <> :'migration_schema'
    and (jsonb_array_length(p.inc) = 0 or n.nspname in (select jsonb_array_elements_text(p.inc)))
    and n.nspname not in (select jsonb_array_elements_text(p.exc))
    and format('%s.%s', n.nspname, c.relname) not in (select jsonb_array_elements_text(p.exct))
    and not exists (
      select 1 from pg_depend d
      where d.classid = 'pg_class'::regclass and d.objid = c.oid and d.deptype = 'e')
)`

// sourcePreflightSQL reports what Start needs to validate the source. A table
// without a replica identity is fatal: once it is published, UPDATE and
// DELETE on it fail on the source.
const sourcePreflightSQL = candidateTablesCTE + `,
-- pg_partition_tree returns no rows for an ordinary table.
leaves as (
  select cand.nspname, cand.relname, cand.oid as leaf, l.relreplident
  from candidates cand
  join pg_class l on l.oid = cand.oid
  where cand.relkind = 'r'
  union all
  select cand.nspname, cand.relname, l.oid, l.relreplident
  from candidates cand
  cross join lateral pg_partition_tree(cand.oid) t
  join pg_class l on l.oid = t.relid
  where cand.relkind = 'p' and t.isleaf
),
unidentified as (
  select distinct format('%I.%I', nspname, relname) as name
  from leaves lv
  where not (
    lv.relreplident = 'f'
    or (lv.relreplident = 'd' and exists (select 1 from pg_index i where i.indrelid = lv.leaf and i.indisprimary))
    or (lv.relreplident = 'i' and exists (select 1 from pg_index i where i.indrelid = lv.leaf and i.indisreplident)))
)
select json_build_object(
  'version', current_setting('server_version_num')::int,
  'walLevel', current_setting('wal_level'),
  'canReplicate', (
    select r.rolsuper or r.rolreplication
      or exists (select 1 from pg_roles rr where rr.rolname = 'rds_replication' and pg_has_role(r.oid, rr.oid, 'member'))
    from pg_roles r where r.rolname = current_user),
  'tables', (select count(*) from candidates),
  'unidentified', coalesce((select json_agg(name order by name) from unidentified), '[]'::json)
)`

const targetPreflightSQL = `
select json_build_object(
  'version', current_setting('server_version_num')::int,
  'superuser', (select rolsuper from pg_roles where rolname = current_user)
)`

const databaseExistsSQL = `select exists (select 1 from pg_database where datname = :'db')`

const createDatabaseSQL = `create database :"db"`

const dropDatabaseSQL = `drop database if exists :"db" with (force)`

// heartbeatTableSQL is identical on both sides: the source table is
// published and replicated into the target table. completed_at is set only
// on the target, after the cutover.
const heartbeatTableSQL = `
create schema if not exists :"migration_schema";
create table if not exists :"migration_schema".heartbeat (
  slot_name text primary key,
  beat_at timestamptz not null,
  token bigint not null default 0,
  completed_at timestamptz
);`

const sourceHeartbeatRowSQL = `
insert into :"migration_schema".heartbeat (slot_name, beat_at) values (:'name', now())
on conflict (slot_name) do update set beat_at = now(), token = 0, completed_at = null;`

const beatSQL = `update :"migration_schema".heartbeat set beat_at = now() where slot_name = :'name';`

const cutoverTokenSQL = `update :"migration_schema".heartbeat set beat_at = now(), token = :'token' where slot_name = :'name';`

// createPublicationSQL also drops a leftover slot of an earlier attempt, which
// would make CREATE SUBSCRIPTION fail.
const createPublicationSQL = `
select pg_drop_replication_slot(slot_name) from pg_replication_slots where slot_name = :'name' and not active;
drop publication if exists :"name";
` + candidateTablesCTE + `
select format('create publication %I for table %s, %I.heartbeat with (publish_via_partition_root = true)',
  :'name', string_agg(format('%I.%I', nspname, relname), ', ' order by nspname, relname), :'migration_schema')
from candidates
\gexec`

// createSubscriptionSQL makes the target connect to the source, copy the
// tables and then apply changes. disable_on_error stays off: it would also
// disable the subscription on a dropped connection (a source restart or
// failover), whereas the apply worker retries and recovers on its own.
// Persistent errors show up in pg_stat_subscription_stats.
const createSubscriptionSQL = `
create subscription :"name" connection :'conninfo' publication :"name"
  with (slot_name = :'name', copy_data = true, create_slot = true, disable_on_error = false, streaming = on);`

// targetStatusSQL reads the subscription, its per-table copy states and the
// replicated heartbeat.
const targetStatusSQL = `
select json_build_object(
  'subExists', s.oid is not null,
  'subEnabled', coalesce(s.subenabled, false),
  'errors', coalesce((select st.apply_error_count + st.sync_error_count from pg_stat_subscription_stats st where st.subid = s.oid), 0),
  'tables', coalesce((
    select json_agg(json_build_object('table', format('%s.%s', n.nspname, c.relname), 'state', r.srsubstate))
    from pg_subscription_rel r
    join pg_class c on c.oid = r.srrelid
    join pg_namespace n on n.oid = c.relnamespace
    where r.srsubid = s.oid), '[]'::json),
  'beatAt', h.beat_at,
  'token', coalesce(h.token, 0),
  'completedAt', h.completed_at
)
from (select 1) one
left join pg_subscription s on s.subname = :'name' and s.subdbid = (select oid from pg_database where datname = current_database())
left join :"migration_schema".heartbeat h on h.slot_name = :'name';`

// sourceStatusSQL returns the source clock (lag is measured against the
// source's own heartbeat timestamps) and, on request, published table sizes.
const sourceStatusSQL = `
select json_build_object(
  'now', now(),
  'sizes', case when :'with_sizes'::boolean then coalesce((
    select json_object_agg(format('%s.%s', pt.schemaname, pt.tablename),
      case when c.relkind = 'p' then (
        select coalesce(sum(pg_table_size(t.relid)), 0) from pg_partition_tree(c.oid) t where t.isleaf)
      else pg_table_size(c.oid) end)
    from pg_publication_tables pt
    join pg_class c on c.oid = format('%I.%I', pt.schemaname, pt.tablename)::regclass
    where pt.pubname = :'name'), '{}'::json) end
);`

const pauseSQL = `alter subscription :"name" disable;`

// resumeSQL clears the error counters so an earlier failure does not mark
// the resumed subscription as failed again.
const resumeSQL = `
select pg_stat_reset_subscription_stats(oid) from pg_subscription where subname = :'name';
alter subscription :"name" enable;`

const enableSQL = `alter subscription :"name" enable;`

// cutoverSourceSQL reads what the target needs after the last change:
// sequence positions and the schemas whose materialized views to refresh.
const cutoverSourceSQL = `
with schemas as (select distinct schemaname from pg_publication_tables where pubname = :'name' and schemaname <> :'migration_schema')
select json_build_object(
  'sequences', coalesce((
    select json_agg(json_build_object('schema', s.schemaname, 'name', s.sequencename, 'value', s.last_value))
    from pg_sequences s where s.last_value is not null and s.schemaname in (select schemaname from schemas)), '[]'::json),
  'schemas', coalesce((select json_agg(schemaname) from schemas), '[]'::json)
);`

const dropSubscriptionSQL = `drop subscription if exists :"name";`

// detachSubscriptionSQL removes the subscription without contacting the
// source; the source slot is then dropped separately.
const detachSubscriptionSQL = `
select format('alter subscription %I disable', subname),
       format('alter subscription %I set (slot_name = none)', subname),
       format('drop subscription %I', subname)
from pg_subscription where subname = :'name'
\gexec`

const syncSequencesSQL = `
select setval(format('%I.%I', s->>'schema', s->>'name')::regclass, (s->>'value')::bigint, true)
from jsonb_array_elements(:'sequences'::jsonb) s
where to_regclass(format('%I.%I', s->>'schema', s->>'name')) is not null;`

const refreshMatviewsSQL = `
select format('refresh materialized view %I.%I', schemaname, matviewname)
from pg_matviews where schemaname in (select jsonb_array_elements_text(:'schemas'::jsonb))
\gexec`

const markCompletedSQL = `
insert into :"migration_schema".heartbeat (slot_name, beat_at, completed_at) values (:'name', now(), now())
on conflict (slot_name) do update set completed_at = now();`

const terminateSlotSQL = `
select pg_terminate_backend(active_pid) from pg_replication_slots where slot_name = :'name' and active_pid is not null;`

const dropSlotSQL = `
select pg_drop_replication_slot(slot_name) from pg_replication_slots where slot_name = :'name';
drop publication if exists :"name";`

const heartbeatExistsSQL = `select to_regclass(format('%I.heartbeat', :'migration_schema')) is not null`

// dropSourceHeartbeatSQL removes this migration's row, and the schema once no
// other migration from the same database uses it.
const dropSourceHeartbeatSQL = `
delete from :"migration_schema".heartbeat where slot_name = :'name';
select format('drop schema %I cascade', :'migration_schema')
where not exists (select 1 from :"migration_schema".heartbeat)
\gexec`

const dropTargetMigrationSchemaSQL = `drop schema if exists :"migration_schema" cascade;`

const recoverTargetSQL = `
select json_build_object(
  'subExists', exists (select 1 from pg_subscription where subname = :'name'
    and subdbid = (select oid from pg_database where datname = current_database())),
  'heartbeat', to_regclass(format('%I.heartbeat', :'migration_schema')) is not null
);`

const completedAtSQL = `select json_build_object('completedAt', (select completed_at from :"migration_schema".heartbeat where slot_name = :'name'));`

const targetTokenSQL = `
select json_build_object('token', coalesce((select token from :"migration_schema".heartbeat where slot_name = :'name'), 0));`
