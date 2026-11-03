import type { MigrationPhase } from '../../api/migrations.types';

export const Messages = {
  phases: {
    provisioning: 'Provisioning',
    starting: 'Starting',
    'full-load': 'Full load',
    'catching-up': 'Catching up',
    replicating: 'Replicating',
    paused: 'Paused',
    finalizing: 'Cutting over',
    completed: 'Completed',
    failed: 'Failed',
    unknown: 'Unknown',
  } satisfies Record<MigrationPhase, string>,
};
