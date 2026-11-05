import type { MigrationAction } from '../../api/migrations.types';
import type { PhaseStep } from './migration-details.types';

export const Messages = {
  back: '← Migrations',
  retry: 'Retry',
  loadError: (message: string) => `Could not load the migration: ${message}`,
  actionError: (message: string) => `The action failed: ${message}`,
  steps: {
    provisioning: 'Provisioning',
    fullLoad: 'Full load',
    replication: 'Change replication',
    cutover: 'Cutover',
    completed: 'Completed',
  } satisfies Record<PhaseStep, string>,
  annotations: {
    paused: 'Replication is paused. Resume to continue applying changes from the source.',
    failed: 'The migration failed. Fix the cause and resume, or delete the migration.',
    unknown: 'The worker cannot be reached, so the current state is unknown. It will be checked again shortly.',
  },
  actions: {
    pause: 'Pause',
    resume: 'Resume',
    cutover: 'Cutover',
    delete: 'Delete',
  } satisfies Record<MigrationAction, string>,
  progress: {
    title: 'Progress',
    dataCopied: 'Data copied',
    copied: (copied: string, total: string, percent: string) => `${copied} of ${total} (${percent})`,
    copiedUnknownTotal: (copied: string) => `${copied} copied`,
    lag: 'Replication lag',
    eventsApplied: 'Events applied',
    lastReplicatedAt: 'Last replicated',
    readyForCutover: 'Ready for cutover',
    notApplicable: '—',
  },
  endpoints: {
    title: 'Endpoints',
    source: 'Source',
    target: 'Target',
    describe: (type: string, label: string) => `${label} (${type})`,
    provider: (label: string, provider: string) => `${label} · ${provider}`,
  },
  options: {
    title: 'Options',
    empty: 'No options.',
  },
  cutoverDialog: {
    title: 'Cut over to the target?',
    description:
      'Cutover finalizes the migration: the worker applies the remaining changes and stops replicating. This cannot be undone.',
    checklist: [
      'Writes to the source are stopped',
      'Replication lag is near zero',
      'Applications are ready to switch to the target',
    ],
    confirm: 'Cut over',
    cancel: 'Cancel',
  },
  deleteDialog: {
    title: 'Delete migration?',
    description: (name: string) =>
      `Deleting "${name}" stops and removes its worker. Data already copied to the target is kept.`,
    activeWarning: 'The migration has not completed. Replication stops immediately and the target may be incomplete.',
    confirm: 'Delete',
    forceConfirm: 'Delete anyway',
    forceHint:
      'Delete anyway removes the migration without that cleanup. Remove what it left behind yourself: for PostgreSQL, drop the replication slot and publication on the source, or the source keeps retaining WAL.',
    cancel: 'Cancel',
  },
};
