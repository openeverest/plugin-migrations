import { describe, expect, it } from 'vitest';
import {
  defaultTidbDmOptions,
  isTidbDmOptionsDraft,
  summarizeTidbDmOptions,
  toTidbDmRequestOptions,
  validateTidbDmOptions,
  validateTidbDmUri,
} from './tidb-dm.utils';

const draft = (overrides: Partial<ReturnType<typeof defaultTidbDmOptions>> = {}) => ({
  ...defaultTidbDmOptions(),
  ...overrides,
});

describe('validateTidbDmOptions', () => {
  it('accepts the defaults and valid settings', () => {
    expect(validateTidbDmOptions(draft())).toEqual({});
    expect(
      validateTidbDmOptions(
        draft({ databases: ['shop', 'crm-2'], excludeTables: ['shop.audit'], storageGiB: ' 50 ', syncThreads: '32' }),
      ),
    ).toEqual({});
  });

  it('rejects names and numbers the backend would reject', () => {
    const errors = validateTidbDmOptions(
      draft({
        databases: ['shop', 'dm_meta', 'openeverest_mig_x', 'a b'],
        excludeTables: ['audit', 'crm.audit'],
        storageGiB: '0',
        copyThreads: '33',
        syncThreads: 'x',
      }),
    );
    expect(Object.keys(errors).sort()).toEqual(['copyThreads', 'databases', 'excludeTables', 'storageGiB', 'syncThreads']);
    expect(errors.databases).toMatch(/dm_meta, openeverest_mig_x, a b/);
    expect(errors.storageGiB).toMatch(/1 to 16384/);
  });
});

describe('toTidbDmRequestOptions', () => {
  it('sends only the numbers that were set', () => {
    expect(toTidbDmRequestOptions(draft({ databases: ['shop'], copyThreads: '8' }))).toEqual({
      databases: ['shop'],
      excludeTables: [],
      recreateDatabases: false,
      storageGiB: 10,
      copyThreads: 8,
    });
  });
});

describe('isTidbDmOptionsDraft', () => {
  it('recognizes its own drafts only', () => {
    expect(isTidbDmOptionsDraft(draft())).toBe(true);
    expect(isTidbDmOptionsDraft({ databases: [], excludeTables: [], recreateDatabases: false, copyThreads: '' })).toBe(
      false,
    );
  });
});

describe('summarizeTidbDmOptions', () => {
  it('describes a stored snapshot', () => {
    expect(
      summarizeTidbDmOptions({
        databases: ['shop'],
        excludeTables: [],
        recreateDatabases: true,
        storageGiB: 25,
        copyThreads: 4,
        syncThreads: 16,
      }),
    ).toEqual([
      { label: 'Databases', value: 'shop' },
      { label: 'Exclude tables', value: 'None' },
      { label: 'Recreate target databases', value: 'Yes' },
      { label: 'Dump storage', value: '25 GiB' },
      { label: 'Copy threads', value: '4' },
      { label: 'Replication threads', value: '16' },
    ]);
  });
});

describe('validateTidbDmUri', () => {
  it('accepts mysql:// with one host', () => {
    expect(validateTidbDmUri('mysql://dm:pw@db1:3306/shop')).toBeUndefined();
    expect(validateTidbDmUri('postgres://db1/shop')).toBeDefined();
  });
});
