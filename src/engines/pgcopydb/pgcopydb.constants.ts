import type { PgcopydbTuningFieldSpec } from './pgcopydb.types';

export const PGCOPYDB_ENGINE_ID = 'pgcopydb';

/** Targets must publish a superuser connection (replication origins, extensions). */
export const PGCOPYDB_TARGET_PROVIDERS = ['provider-percona-postgresql'];

export const PGCOPYDB_SOURCE_PROVIDERS = ['provider-percona-postgresql'];

export const POSTGRES_URI_SCHEMES = ['postgres://', 'postgresql://'];

/** Unquoted PostgreSQL identifier; quoted names are not supported in filters yet. */
export const IDENTIFIER_PATTERN = /^[A-Za-z_][A-Za-z0-9_$]{0,62}$/;

export const DATABASE_PATTERN = /^[A-Za-z_][A-Za-z0-9_-]{0,62}$/;

/** Holds the worker's heartbeat on the source; never migrated. */
export const RESERVED_SCHEMA = 'pgcopydb';

export const PGCOPYDB_TUNING_FIELDS: readonly PgcopydbTuningFieldSpec[] = [
  { field: 'tableJobs', min: 1, max: 64 },
  { field: 'indexJobs', min: 1, max: 64 },
];
