export const Messages = {
  label: 'PostgreSQL → PostgreSQL',
  description:
    'Native logical replication: copies the schema and data, then streams ongoing changes from the source until you cut over.',
  externalSource: {
    label: 'External PostgreSQL',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'postgresql://user:password@db1:5432/shop?sslmode=require',
    uriHelperText:
      'The source needs wal_level=logical, and the user REPLICATION and ownership of the tables (or superuser). Both the worker and the target instance must reach this host. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA the worker uses to verify the source. The target instance connects with TLS but cannot verify the source certificate.',
  },
  uriRequired: 'Enter a connection URI.',
  uriScheme: 'The URI must start with postgres:// or postgresql://.',
  uriHost: 'The URI must include a host.',
  uriMultiHost: 'Use a single host: the primary server.',
  database: {
    label: 'Database',
    helper:
      'The database to migrate; it keeps its name on the target. Required for an OpenEverest source. Leave empty to use the database from the connection URI.',
    invalid: 'Use letters, digits, "_" or "-", starting with a letter or "_".',
  },
  includeSchemas: {
    label: 'Include schemas',
    helper: 'Leave empty to migrate every schema. Press Enter to add.',
  },
  excludeSchemas: {
    label: 'Exclude schemas',
    helper: 'Schemas to skip. Press Enter to add.',
  },
  excludeTables: {
    label: 'Exclude tables',
    helper: 'Tables to skip, as schema.table. Press Enter to add.',
  },
  invalidSchemas: (names: string[]) => `Invalid schema: ${names.join(', ')}. Use unquoted PostgreSQL names.`,
  invalidTables: (names: string[]) => `Invalid table: ${names.join(', ')}. Use schema.table with unquoted names.`,
  reservedSchema: (name: string) => `The "${name}" schema is reserved for the migration worker.`,
  includeAndExclude: 'Use either include or exclude schemas, not both.',
  recreateDatabase: {
    label: 'Recreate the target database',
    helper:
      'Drops the target database if it already exists, then creates it empty. Everything in it is lost. Leave off to fail when the database already has objects with the same names.',
  },
  notes:
    'Schema changes (DDL) made on the source during the migration are not replicated. Every table needs a primary key or REPLICA IDENTITY FULL. Sequences are synced and materialized views refreshed at cutover.',
  summary: {
    database: 'Database',
    fromUri: 'From the connection URI',
    includeSchemas: 'Include schemas',
    excludeSchemas: 'Exclude schemas',
    excludeTables: 'Exclude tables',
    recreateDatabase: 'Recreate target database',
    all: 'All',
    none: 'None',
    yes: 'Yes',
    no: 'No',
  },
};
