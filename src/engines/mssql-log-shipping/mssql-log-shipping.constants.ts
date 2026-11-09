export const MSSQL_LOG_SHIPPING_ENGINE_ID = 'mssql-log-shipping';

/** provider-mssql publishes sa, which can back up, restore and create logins. */
export const MSSQL_PROVIDERS = ['mssql'];

export const MSSQL_URI_SCHEMES = ['sqlserver://'];

/** Stricter than SQL Server: names travel through T-SQL, S3 object keys and file names. */
export const NAME_PATTERN = /^[A-Za-z0-9_][A-Za-z0-9_$-]{0,99}$/;

export const SYSTEM_DATABASES = ['master', 'model', 'msdb', 'tempdb'];

export const MAX_DATABASES = 50;

export const LOG_BACKUP_INTERVAL = { min: 5, max: 300, default: 10 };
