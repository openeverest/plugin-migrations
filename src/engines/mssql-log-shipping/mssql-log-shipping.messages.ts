export const Messages = {
  label: 'SQL Server → SQL Server',
  description:
    'Log shipping through S3: a full backup is restored on the target, then log backups every few seconds until you cut over. Needs SQL Server 2022 or later.',
  externalSource: {
    label: 'External SQL Server',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'sqlserver://migrator:password@db1:1433?database=shop',
    uriHelperText:
      'SQL Server 2022+ with the databases in the FULL recovery model. The user must be a sysadmin: it backs up the databases to S3 and creates an S3 credential. The server must reach and trust the backup storage. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA the worker uses to verify the source. Without it, the connection is encrypted but the certificate is not verified.',
  },
  databases: {
    label: 'Databases',
    helper:
      'Databases to migrate; they keep their names on the target. Required for an OpenEverest source. Leave empty to use the database from the connection URI. Press Enter to add.',
  },
  invalidDatabases: (names: string[]) =>
    `Invalid database: ${names.join(', ')}. Use letters, digits, "_", "$" or "-"; system databases cannot be migrated.`,
  tooManyDatabases: (max: number) => `Migrate at most ${max} databases at once.`,
  recreateDatabases: {
    label: 'Recreate the target databases',
    helper:
      'Drops these databases on the target if they exist. Everything in them is lost. Leave off to fail when a database already exists.',
  },
  copyLogins: {
    label: 'Copy logins',
    helper:
      "At the cutover, creates the SQL logins of the databases' users on the target, with the same SID and password. Windows logins and server roles are not copied.",
  },
  leaveSourceRestoring: {
    label: 'Leave the source databases restoring',
    helper:
      'The cutover takes a tail-log backup WITH NORECOVERY: the source databases go offline, so no write can be lost. Not possible for databases in an availability group.',
  },
  ignoreRecentLogBackups: {
    label: 'Other log backups are disabled',
    helper:
      'The preflight fails when other log backups ran on the source in the last 24 hours. Enable this once you have disabled them.',
  },
  advanced: 'Advanced',
  advancedHelper: 'Tune log shipping.',
  logBackupInterval: {
    label: 'Log backup interval (seconds)',
    helper:
      'How often a log backup is taken and restored. The cutover is offered once the target is within 15 seconds of the source.',
  },
  integerRange: (min: number, max: number) => `Enter a whole number from ${min} to ${max}.`,
  notes:
    'The migration owns the log chain of these databases: any other log backup (for an OpenEverest source: point-in-time recovery) breaks it. The migration stops it with an error, and you must start again. Server objects other than logins (jobs, linked servers) are not migrated.',
  summary: {
    databases: 'Databases',
    fromUri: 'From the connection URI',
    recreateDatabases: 'Recreate target databases',
    copyLogins: 'Copy logins',
    leaveSourceRestoring: 'Leave source restoring',
    ignoreRecentLogBackups: 'Other log backups disabled',
    logBackupInterval: 'Log backup interval',
    seconds: (value: number) => `${value} s`,
    yes: 'Yes',
    no: 'No',
  },
};
