export const TIDB_DM_ENGINE_ID = 'tidb-dm';

/** provider-tidb publishes root, which DM needs to create databases and its checkpoints. */
export const TIDB_DM_TARGET_PROVIDERS = ['tidb'];

export const TIDB_DM_SOURCE_PROVIDERS = ['percona-xtradb-cluster'];

export const MYSQL_URI_SCHEMES = ['mysql://'];

/** Stricter than MySQL: names travel through SQL and DM's block-allow list. */
export const NAME_PATTERN = /^[A-Za-z0-9_$-]{1,64}$/;

/** System databases of MySQL and TiDB, and those of the migration machinery. */
export const RESERVED_DATABASES = [
  'mysql',
  'sys',
  'information_schema',
  'performance_schema',
  'metrics_schema',
  'openeverest_migration',
  'dm_meta',
  'lightning_task_info',
];

/** Each migration's heartbeat database on the source ("openeverest_mig_…"). */
export const RESERVED_DATABASE_PREFIX = 'openeverest_mig_';

export const STORAGE_GIB = { min: 1, max: 16384, default: 10 };
export const COPY_THREADS = { min: 1, max: 32, default: 4 };
export const SYNC_THREADS = { min: 1, max: 64, default: 16 };
