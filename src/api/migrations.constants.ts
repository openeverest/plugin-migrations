import type { MigrationAction, MigrationPhase } from './migrations.types';

// Records keep the lists exhaustive: adding a phase or action to the union
// fails compilation until it is listed here.
const PHASES: Record<MigrationPhase, true> = {
  provisioning: true,
  starting: true,
  'full-load': true,
  'catching-up': true,
  replicating: true,
  paused: true,
  finalizing: true,
  completed: true,
  failed: true,
  unknown: true,
};

const ACTIONS: Record<MigrationAction, true> = {
  pause: true,
  resume: true,
  cutover: true,
  delete: true,
};

export function isMigrationPhase(value: unknown): value is MigrationPhase {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(PHASES, value);
}

export function isMigrationAction(value: unknown): value is MigrationAction {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(ACTIONS, value);
}
