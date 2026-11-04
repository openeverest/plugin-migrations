export const POSTGRES_LOGICAL_ENGINE_ID = 'postgres-logical';

/** Targets must publish a superuser connection, which creates the subscription. */
export const POSTGRES_LOGICAL_TARGET_PROVIDERS = ['provider-percona-postgresql'];

export const POSTGRES_LOGICAL_SOURCE_PROVIDERS = ['provider-percona-postgresql'];

export const POSTGRES_URI_SCHEMES = ['postgres://', 'postgresql://'];

/** Unquoted PostgreSQL identifier; quoted names are not supported in filters yet. */
export const IDENTIFIER_PATTERN = /^[A-Za-z_][A-Za-z0-9_$]{0,62}$/;

export const DATABASE_PATTERN = /^[A-Za-z_][A-Za-z0-9_-]{0,62}$/;

/** Holds the worker's heartbeat table on the source and target; never migrated. */
export const RESERVED_SCHEMA = 'openeverest_migration';
