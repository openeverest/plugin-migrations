import { describe, expect, it } from 'vitest';
import type { Migration, MigrationPhase, MigrationProgress } from '../../api/migrations.types';
import { PHASE_STEPS } from './migration-details.constants';
import { phaseStepperState } from './migration-details.utils';

const IDLE_PROGRESS: MigrationProgress = {
  bytesTotal: 0,
  bytesCopied: 0,
  lagSeconds: 0,
  eventsApplied: 0,
  readyForCutover: false,
};

function migration(phase: MigrationPhase, overrides: Partial<Migration> = {}): Migration {
  return {
    id: 'mig-1',
    name: 'orders',
    engine: 'pcsm',
    source: { type: 'external', display: 'db1:27017' },
    target: { type: 'instance', k8sCluster: 'main', namespace: 'shop', name: 'orders' },
    options: {},
    createdAt: '2026-11-03T10:00:00Z',
    phase,
    message: '',
    error: '',
    progress: IDLE_PROGRESS,
    allowedActions: [],
    ...overrides,
  };
}

describe('phaseStepperState', () => {
  it('maps regular phases onto the stepper', () => {
    const steps: Array<[MigrationPhase, number]> = [
      ['provisioning', 0],
      ['starting', 0],
      ['full-load', 1],
      ['catching-up', 2],
      ['replicating', 2],
      ['finalizing', 3],
    ];
    for (const [phase, activeStep] of steps) {
      expect(phaseStepperState(migration(phase))).toEqual({ activeStep });
    }
  });

  it('marks every step as done when completed', () => {
    expect(phaseStepperState(migration('completed'))).toEqual({ activeStep: PHASE_STEPS.length });
  });

  it('keeps a paused migration on change replication with an annotation', () => {
    expect(phaseStepperState(migration('paused'))).toEqual({ activeStep: 2, annotation: 'paused' });
  });

  it('infers where a failed migration stopped from its progress', () => {
    expect(phaseStepperState(migration('failed'))).toEqual({ activeStep: 0, failedStep: 0, annotation: 'failed' });
    expect(phaseStepperState(migration('failed', { startedAt: '2026-11-03T10:00:20Z' }))).toEqual({
      activeStep: 1,
      failedStep: 1,
      annotation: 'failed',
    });
    expect(
      phaseStepperState(
        migration('failed', {
          startedAt: '2026-11-03T10:00:20Z',
          progress: { ...IDLE_PROGRESS, bytesTotal: 100, bytesCopied: 100 },
        }),
      ),
    ).toEqual({ activeStep: 2, failedStep: 2, annotation: 'failed' });
  });

  it('annotates an unreachable worker without marking a failure', () => {
    expect(
      phaseStepperState(migration('unknown', { progress: { ...IDLE_PROGRESS, readyForCutover: true } })),
    ).toEqual({ activeStep: 2, annotation: 'unknown' });
  });
});
