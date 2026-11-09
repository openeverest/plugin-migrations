export const MYSQL_REPLICATION_ENGINE_ID = 'mysql-replication';

/** Targets must publish root, which configures replication on a PXC node. */
export const MYSQL_REPLICATION_TARGET_PROVIDERS = ['percona-xtradb-cluster'];

export const MYSQL_REPLICATION_SOURCE_PROVIDERS = ['percona-xtradb-cluster'];

export const MYSQL_URI_SCHEMES = ['mysql://'];

/** Stricter than MySQL: names travel through SQL, replication filters and MySQL Shell. */
export const NAME_PATTERN = /^[A-Za-z0-9_$-]{1,64}$/;

/** Exist on every server; openeverest_migration holds the worker's heartbeat and state. */
export const SYSTEM_DATABASES = ['mysql', 'sys', 'information_schema', 'performance_schema', 'openeverest_migration'];

export const COPY_THREADS = { min: 1, max: 32, default: 4 };
