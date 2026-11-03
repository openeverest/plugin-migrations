import { describe, expect, it } from 'vitest';
import type { Migration, MigrationPhase } from '../../api/migrations.types';
import { summarizeProgress } from './migrations-page.utils';

function migration(phase: MigrationPhase): Migration {
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
    progress: { bytesTotal: 1_000, bytesCopied: 250, lagSeconds: 7, eventsApplied: 0, readyForCutover: false },
    allowedActions: [],
  };
}

describe('summarizeProgress', () => {
  it('reports copied bytes during the full load', () => {
    expect(summarizeProgress(migration('full-load'))).toEqual({
      kind: 'copy',
      bytesCopied: 250,
      bytesTotal: 1_000,
      percent: 25,
    });
  });

  it('reports lag during change replication', () => {
    expect(summarizeProgress(migration('catching-up'))).toEqual({ kind: 'lag', lagSeconds: 7 });
    expect(summarizeProgress(migration('replicating'))).toEqual({ kind: 'lag', lagSeconds: 7 });
  });

  it('reports nothing in other phases', () => {
    const phases: MigrationPhase[] = ['provisioning', 'paused', 'completed', 'failed', 'unknown'];
    for (const phase of phases) {
      expect(summarizeProgress(migration(phase))).toEqual({ kind: 'none' });
    }
  });
});
