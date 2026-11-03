import type { MigrationPhase } from '../../api/migrations.types';
import type { PhaseStep } from './migration-details.types';

export const DETAILS_POLL_INTERVAL_MS = 3_000;

export const PHASE_STEPS: readonly PhaseStep[] = ['provisioning', 'fullLoad', 'replication', 'cutover', 'completed'];

export const PHASE_STEP_INDEX: Partial<Record<MigrationPhase, number>> = {
  provisioning: 0,
  starting: 0,
  'full-load': 1,
  'catching-up': 2,
  replicating: 2,
  // Pause is only possible during change replication.
  paused: 2,
  finalizing: 3,
  completed: PHASE_STEPS.length,
};

export const CHANGE_REPLICATION_PHASES: readonly MigrationPhase[] = ['catching-up', 'replicating', 'paused'];
