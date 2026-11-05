import { describe, expect, it } from 'vitest';
import {
  defaultPcsmOptions,
  isPcsmOptionsDraft,
  summarizePcsmOptions,
  toPcsmRequestOptions,
  validateMongoUri,
  validatePcsmOptions,
} from './pcsm.utils';
import type { PcsmOptionsDraft } from './pcsm.types';

function draft(overrides: Partial<PcsmOptionsDraft> = {}): PcsmOptionsDraft {
  return { ...defaultPcsmOptions(), ...overrides };
}

describe('validatePcsmOptions', () => {
  it('accepts the defaults', () => {
    expect(validatePcsmOptions(defaultPcsmOptions())).toEqual({});
  });

  it('accepts db, db.* and db.collection namespaces', () => {
    expect(validatePcsmOptions(draft({ includeNamespaces: ['shop', 'shop.*', 'shop.orders', 'shop.system.users'] }))).toEqual(
      {},
    );
  });

  it('rejects malformed, partially wildcarded and duplicate namespaces', () => {
    const errors = validatePcsmOptions(
      draft({ includeNamespaces: ['shop.ord*', '*.orders', 'sh$op'], excludeNamespaces: ['logs', 'logs'] }),
    );
    expect(errors.includeNamespaces).toMatch(/shop\.ord\*, \*\.orders, sh\$op/);
    expect(errors.excludeNamespaces).toMatch(/Duplicate namespace: logs/);
  });

  it('enforces the documented integer ranges and allows empty (PCSM default)', () => {
    const errors = validatePcsmOptions(
      draft({
        tuning: {
          cloneNumParallelCollections: '65',
          cloneNumReadWorkers: '256',
          cloneNumInsertWorkers: '0',
          replicationNumWorkers: '1.5',
        },
      }),
    );
    expect(Object.keys(errors).sort()).toEqual([
      'cloneNumInsertWorkers',
      'cloneNumParallelCollections',
      'replicationNumWorkers',
    ]);
    expect(errors.cloneNumParallelCollections).toBe('Enter a whole number from 1 to 64.');
    expect(errors.replicationNumWorkers).toBe('Enter a whole number from 1 to 256.');
  });
});

describe('toPcsmRequestOptions', () => {
  it('omits empty tuning groups', () => {
    expect(toPcsmRequestOptions(draft({ includeNamespaces: ['shop.*'] }))).toEqual({
      includeNamespaces: ['shop.*'],
      excludeNamespaces: [],
      pauseOnInitialSync: false,
    });
  });

  it('nests tuning values as in the contract', () => {
    const options = toPcsmRequestOptions(
      draft({
        pauseOnInitialSync: true,
        tuning: {
          cloneNumParallelCollections: ' 8 ',
          cloneNumReadWorkers: '',
          cloneNumInsertWorkers: '16',
          replicationNumWorkers: '4',
        },
      }),
    );
    expect(options).toEqual({
      includeNamespaces: [],
      excludeNamespaces: [],
      pauseOnInitialSync: true,
      clone: { numParallelCollections: 8, numInsertWorkers: 16 },
      replication: { numWorkers: 4 },
    });
  });
});

describe('summarizePcsmOptions', () => {
  it('describes a backend snapshot, falling back to defaults for missing values', () => {
    const summary = summarizePcsmOptions({
      includeNamespaces: ['shop.*'],
      excludeNamespaces: [],
      pauseOnInitialSync: false,
      clone: { numReadWorkers: 12 },
      replication: {},
    });
    expect(summary).toEqual([
      { label: 'Include namespaces', value: 'shop.*' },
      { label: 'Exclude namespaces', value: 'None' },
      { label: 'Pause on initial sync', value: 'No' },
      { label: 'Parallel collections', value: 'PCSM default' },
      { label: 'Clone read workers', value: '12' },
      { label: 'Clone insert workers', value: 'PCSM default' },
      { label: 'Replication workers', value: 'PCSM default' },
    ]);
    expect(summarizePcsmOptions(undefined)[0]).toEqual({ label: 'Include namespaces', value: 'All' });
  });
});

describe('isPcsmOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isPcsmOptionsDraft(defaultPcsmOptions())).toBe(true);
    expect(isPcsmOptionsDraft({ includeNamespaces: [], excludeNamespaces: [], pauseOnInitialSync: false })).toBe(false);
    expect(isPcsmOptionsDraft({ ...defaultPcsmOptions(), includeNamespaces: [1] })).toBe(false);
  });
});

describe('validateMongoUri', () => {
  it('accepts mongodb:// and mongodb+srv:// URIs', () => {
    expect(validateMongoUri('mongodb://u:p@db1:27017,db2:27017/?replicaSet=rs0')).toBeUndefined();
    expect(validateMongoUri('mongodb+srv://cluster.example.com')).toBeUndefined();
  });

  it('rejects empty, wrong-scheme and host-less URIs', () => {
    expect(validateMongoUri('  ')).toBe('Enter a connection URI.');
    expect(validateMongoUri('postgres://db:5432')).toMatch(/must start with/);
    expect(validateMongoUri('mongodb://user:pass@/db')).toMatch(/at least one host/);
  });
});
