import type { Migration } from '../../api/migrations.types';
import { CHANGE_REPLICATION_PHASES, PHASE_STEP_INDEX } from './migration-details.constants';
import type { PhaseAnnotation, PhaseStepperState } from './migration-details.types';

// failed/unknown do not say where the migration stopped, so infer the step from
// what the worker reported last.
function inferStepFromProgress({ progress, startedAt }: Migration): number {
  if (progress.readyForCutover) return 2;
  if (progress.bytesTotal > 0 && progress.bytesCopied >= progress.bytesTotal) return 2;
  if (startedAt) return 1;
  return 0;
}

export function phaseStepperState(migration: Migration): PhaseStepperState {
  const { phase } = migration;
  const annotation: PhaseAnnotation | undefined =
    phase === 'paused' || phase === 'failed' || phase === 'unknown' ? phase : undefined;
  const activeStep = PHASE_STEP_INDEX[phase] ?? inferStepFromProgress(migration);
  return {
    activeStep,
    ...(phase === 'failed' ? { failedStep: activeStep } : {}),
    ...(annotation ? { annotation } : {}),
  };
}

export function isChangeReplicationPhase(migration: Migration): boolean {
  return CHANGE_REPLICATION_PHASES.includes(migration.phase);
}
