import { describe, expect, it } from 'vitest';
import {
  defaultMysqlReplicationOptions,
  isMysqlReplicationOptionsDraft,
  summarizeMysqlReplicationOptions,
  toMysqlReplicationRequestOptions,
  validateMysqlReplicationOptions,
  validateMysqlUri,
} from './mysql-replication.utils';

const draft = (overrides: Partial<ReturnType<typeof defaultMysqlReplicationOptions>> = {}) => ({
  ...defaultMysqlReplicationOptions(),
  ...overrides,
});

describe('validateMysqlReplicationOptions', () => {
  it('accepts the defaults and valid filters', () => {
    expect(validateMysqlReplicationOptions(draft())).toEqual({});
    expect(
      validateMysqlReplicationOptions(
        draft({ databases: ['shop', 'crm-2'], excludeTables: ['shop.audit_log'], copyThreads: ' 8 ' }),
      ),
    ).toEqual({});
  });

  it('rejects names the backend would reject', () => {
    const errors = validateMysqlReplicationOptions(
      draft({ databases: ['shop', 'mysql', 'a b'], excludeTables: ['audit', 'crm.audit'], copyThreads: '33' }),
    );
    expect(Object.keys(errors).sort()).toEqual(['copyThreads', 'databases', 'excludeTables']);
    expect(errors.databases).toMatch(/mysql, a b/);
    expect(errors.excludeTables).toMatch(/audit, crm\.audit/);
    expect(errors.copyThreads).toMatch(/1 to 32/);
  });

  it('leaves excluded tables to the backend when databases come from the URI', () => {
    expect(validateMysqlReplicationOptions(draft({ excludeTables: ['shop.audit'] }))).toEqual({});
    expect(validateMysqlReplicationOptions(draft({ excludeTables: ['sys.audit'] })).excludeTables).toBeDefined();
  });
});

describe('toMysqlReplicationRequestOptions', () => {
  it('omits an empty copy thread count', () => {
    expect(toMysqlReplicationRequestOptions(draft({ databases: ['shop'] }))).toEqual({
      databases: ['shop'],
      excludeTables: [],
      recreateDatabases: false,
    });
    expect(toMysqlReplicationRequestOptions(draft({ copyThreads: '8', recreateDatabases: true }))).toMatchObject({
      copyThreads: 8,
      recreateDatabases: true,
    });
  });
});

describe('summarizeMysqlReplicationOptions', () => {
  it('describes a stored snapshot', () => {
    const summary = summarizeMysqlReplicationOptions({ databases: ['shop', 'crm'], excludeTables: [], copyThreads: 4 });
    expect(Object.fromEntries(summary.map(({ label, value }) => [label, value]))).toEqual({
      Databases: 'shop, crm',
      'Exclude tables': 'None',
      'Recreate target databases': 'No',
      'Copy threads': '4',
    });
  });
});

describe('isMysqlReplicationOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isMysqlReplicationOptionsDraft(defaultMysqlReplicationOptions())).toBe(true);
    expect(isMysqlReplicationOptionsDraft({ ...defaultMysqlReplicationOptions(), copyThreads: 4 })).toBe(false);
    expect(isMysqlReplicationOptionsDraft({ database: 'shop' })).toBe(false);
  });
});

describe('validateMysqlUri', () => {
  it('accepts single-host mysql URIs', () => {
    expect(validateMysqlUri('mysql://repl:secret@db1:3306/shop?ssl-mode=REQUIRED')).toBeUndefined();
    expect(validateMysqlUri('mysql://db1/shop')).toBeUndefined();
  });

  it('rejects other schemes, missing hosts and multi-host URIs', () => {
    expect(validateMysqlUri('')).toMatch(/Enter/);
    expect(validateMysqlUri('postgres://db1/shop')).toMatch(/mysql:\/\//);
    expect(validateMysqlUri('mysql://user@/shop')).toMatch(/host/);
    expect(validateMysqlUri('mysql://db1:3306,db2:3306/shop')).toMatch(/single host/);
  });
});
