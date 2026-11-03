import type { PcsmTuningField } from './pcsm.types';

export const Messages = {
  label: 'MongoDB → MongoDB',
  description:
    'Percona ClusterSync for MongoDB copies existing data, then streams ongoing changes until you cut over.',
  externalSource: {
    label: 'External MongoDB',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'mongodb://user:password@db1:27017,db2:27017/?replicaSet=rs0',
    uriHelperText: 'The worker must be able to reach these hosts. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText: 'PEM-encoded CA used to verify the source when TLS is enabled.',
  },
  uriRequired: 'Enter a connection URI.',
  uriScheme: 'The URI must start with mongodb:// or mongodb+srv://.',
  uriHost: 'The URI must include at least one host.',
  includeNamespaces: {
    label: 'Include namespaces',
    helper: 'Leave empty to migrate all databases. Use db, db.* or db.collection. Press Enter to add.',
  },
  excludeNamespaces: {
    label: 'Exclude namespaces',
    helper: 'Namespaces to skip. Use db, db.* or db.collection. Press Enter to add.',
  },
  invalidNamespaces: (namespaces: string[]) => `Invalid namespace: ${namespaces.join(', ')}. Use db, db.* or db.collection.`,
  duplicateNamespaces: (namespaces: string[]) => `Duplicate namespace: ${namespaces.join(', ')}.`,
  pauseOnInitialSync: {
    label: 'Pause when the initial sync completes',
    helper: 'Stops before streaming ongoing changes so you can inspect the target first.',
  },
  advanced: 'Advanced',
  advancedHelper: 'Tuning for the worker. Leave a field empty to use the PCSM default.',
  integerRange: (min: number, max: number) => `Enter a whole number from ${min} to ${max}.`,
  tuning: {
    cloneNumParallelCollections: { label: 'Parallel collections', helper: 'Collections cloned at the same time (1–64).' },
    cloneNumReadWorkers: { label: 'Clone read workers', helper: 'Readers per collection during the clone (1–256).' },
    cloneNumInsertWorkers: { label: 'Clone insert workers', helper: 'Writers per collection during the clone (1–256).' },
    replicationNumWorkers: { label: 'Replication workers', helper: 'Workers that apply change events (1–256).' },
  } satisfies Record<PcsmTuningField, { label: string; helper: string }>,
  summary: {
    includeNamespaces: 'Include namespaces',
    excludeNamespaces: 'Exclude namespaces',
    pauseOnInitialSync: 'Pause on initial sync',
    all: 'All',
    none: 'None',
    yes: 'Yes',
    no: 'No',
    pcsmDefault: 'PCSM default',
  },
};
