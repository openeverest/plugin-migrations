import type { PgcopydbTuningField } from './pgcopydb.types';

export const Messages = {
  label: 'PostgreSQL → PostgreSQL',
  description: 'pgcopydb copies a database, then streams ongoing changes with logical decoding until you cut over.',
  externalSource: {
    label: 'External PostgreSQL',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'postgresql://user:password@db1:5432/shop?sslmode=require',
    uriHelperText:
      'The worker must reach this server. The user needs REPLICATION and read access, and the server wal_level=logical. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText: 'PEM-encoded CA used to verify the source server certificate.',
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
  dropIfExists: {
    label: 'Replace existing objects on the target',
    helper: 'Drops tables and other objects of the same name in the target database before copying.',
  },
  advanced: 'Advanced',
  advancedHelper: 'Tuning for the worker. Leave a field empty to use the pgcopydb default.',
  integerRange: (min: number, max: number) => `Enter a whole number from ${min} to ${max}.`,
  tuning: {
    tableJobs: { label: 'Table jobs', helper: 'Tables copied at the same time (1–64).' },
    indexJobs: { label: 'Index jobs', helper: 'Indexes built at the same time (1–64).' },
  } satisfies Record<PgcopydbTuningField, { label: string; helper: string }>,
  summary: {
    database: 'Database',
    fromUri: 'From the connection URI',
    includeSchemas: 'Include schemas',
    excludeSchemas: 'Exclude schemas',
    excludeTables: 'Exclude tables',
    dropIfExists: 'Replace existing objects',
    all: 'All',
    none: 'None',
    yes: 'Yes',
    no: 'No',
    pgcopydbDefault: 'pgcopydb default',
  },
};
