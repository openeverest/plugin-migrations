export const Messages = {
  label: 'MySQL → MySQL',
  description:
    'Native GTID replication: MySQL Shell copies the databases, then the target replicates ongoing changes, schema changes included, until you cut over.',
  externalSource: {
    label: 'External MySQL',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'mysql://user:password@db1:3306/shop?ssl-mode=REQUIRED',
    uriHelperText:
      'MySQL 8.0+ with gtid_mode=ON and binlog_format=ROW. The user needs REPLICATION SLAVE and read access to the databases. Both the worker and the target instance must reach this host. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA the worker uses to verify the source. The target instance replicates over TLS but cannot verify the source certificate.',
  },
  databases: {
    label: 'Databases',
    helper:
      'Databases to migrate; they keep their names on the target. Required for an OpenEverest source. Leave empty to use the database from the connection URI. Press Enter to add.',
  },
  excludeTables: {
    label: 'Exclude tables',
    helper: 'Tables to skip, as database.table. No migrated table may reference them with a foreign key. Press Enter to add.',
  },
  invalidDatabases: (names: string[]) =>
    `Invalid database: ${names.join(', ')}. Use letters, digits, "_", "$" or "-"; system databases cannot be migrated.`,
  invalidTables: (names: string[]) => `Invalid table: ${names.join(', ')}. Use database.table with a migrated database.`,
  recreateDatabases: {
    label: 'Recreate the target databases',
    helper:
      'Drops these databases on the target if they exist. Everything in them is lost. Leave off to fail when a database already exists.',
  },
  advanced: 'Advanced',
  advancedHelper: 'Tune the initial copy.',
  copyThreads: {
    label: 'Copy threads',
    helper: 'Threads reading from the source, and as many writing to the target.',
  },
  integerRange: (min: number, max: number) => `Enter a whole number from ${min} to ${max}.`,
  notes:
    'Schema changes, views, routines, triggers and events are replicated. User accounts are not: create them on the target. On PXC every table needs a primary key and InnoDB. The source must keep its binary logs until the copy finishes.',
  summary: {
    databases: 'Databases',
    fromUri: 'From the connection URI',
    excludeTables: 'Exclude tables',
    recreateDatabases: 'Recreate target databases',
    copyThreads: 'Copy threads',
    none: 'None',
    yes: 'Yes',
    no: 'No',
  },
};
