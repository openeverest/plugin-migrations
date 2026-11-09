export const Messages = {
  label: 'MariaDB → MariaDB',
  description:
    'Native GTID replication: mydumper copies the databases, then the target replicates ongoing changes, schema changes included, until you cut over.',
  externalSource: {
    label: 'External MariaDB',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'mariadb://repl:secret@db1:3306/shop?ssl-mode=REQUIRED',
    uriHelperText:
      'MariaDB 10.6+ with log_bin, binlog_format=ROW and a server_id different from the target. The user needs REPLICATION SLAVE, BINLOG MONITOR, RELOAD and read access to the databases. Both the worker and the target instance must reach this host. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA the worker uses to verify the source. The target instance replicates over TLS but cannot verify the source certificate.',
  },
  notes:
    'Schema changes, views, routines, triggers and events are replicated. User accounts are not: create them on the target. The target must be a standalone or Galera instance; on Galera every table needs a primary key and InnoDB. The source must keep its binary logs until the copy finishes.',
  galeraAccountsWarning:
    'Galera targets: do not create, change or drop users, roles or grants on the source while the migration runs. Because of a MariaDB limitation, such statements reach the other Galera nodes of the target, although they are filtered on the node that replicates, so the nodes end up with different accounts. An OpenEverest source does this itself when its users are changed.',
};
