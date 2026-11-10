export const Messages = {
  label: 'MySQL → TiDB',
  description:
    'TiDB Data Migration (DM): dumps the databases, loads them into TiDB, then replicates ongoing changes from the binary log until you cut over.',
  externalSource: {
    label: 'External MySQL',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'mysql://dm:password@db1:3306/shop',
    uriHelperText:
      'MySQL 5.7 to 8.4 with binlog_format=ROW and binlog_row_image=FULL. The user needs SELECT, RELOAD, REPLICATION SLAVE and REPLICATION CLIENT, and CREATE for the heartbeat database. The worker must reach this host. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA used to verify the source. DM checks the host name too. Without a CA, DM connects without TLS, so ssl-mode=REQUIRED needs one.',
  },
  databases: {
    label: 'Databases',
    helper:
      'Databases to migrate; they keep their names on the target. Required for an OpenEverest source. Leave empty to use the database from the connection URI. Press Enter to add.',
  },
  excludeTables: {
    label: 'Exclude tables',
    helper: 'Tables to skip, as database.table. They are neither copied nor replicated. Press Enter to add.',
  },
  invalidDatabases: (names: string[]) =>
    `Invalid database: ${names.join(', ')}. Use letters, digits, "_", "$" or "-"; system databases cannot be migrated.`,
  invalidTables: (names: string[]) => `Invalid table: ${names.join(', ')}. Use database.table with a migrated database.`,
  recreateDatabases: {
    label: 'Recreate the target databases',
    helper:
      'Drops these databases on the target if they exist. Everything in them is lost. Leave off to fail when a database already has tables.',
  },
  storageGiB: {
    label: 'Dump storage (GiB)',
    helper:
      'Volume of the migration worker. DM stages a dump of the databases on it before loading it into TiDB, so it must hold about as much as the source data. Deleted with the migration.',
  },
  advanced: 'Advanced',
  advancedHelper: 'Tune the copy and the replication.',
  copyThreads: {
    label: 'Copy threads',
    helper: 'Threads dumping from the source, and as many loading into TiDB.',
  },
  syncThreads: {
    label: 'Replication threads',
    helper: 'Threads applying replicated changes on TiDB.',
  },
  integerRange: (min: number, max: number) => `Enter a whole number from ${min} to ${max}.`,
  notes:
    'Tables, data and schema changes are migrated; views are created on TiDB once replication runs and again at the cutover. TiDB has no stored routines, triggers or events, so they are not migrated, and FULLTEXT indexes are dropped. Foreign keys with CASCADE or SET NULL actions are not supported. User accounts are not migrated: create them on TiDB. The source must keep its binary logs until the copy finishes.',
  summary: {
    databases: 'Databases',
    fromUri: 'From the connection URI',
    excludeTables: 'Exclude tables',
    recreateDatabases: 'Recreate target databases',
    storageGiB: 'Dump storage',
    copyThreads: 'Copy threads',
    syncThreads: 'Replication threads',
    none: 'None',
    yes: 'Yes',
    no: 'No',
    gib: (value: number) => `${value} GiB`,
  },
};
