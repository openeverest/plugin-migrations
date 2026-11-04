import { describe, expect, it } from 'vitest';
import {
  defaultPostgresLogicalOptions,
  isPostgresLogicalOptionsDraft,
  summarizePostgresLogicalOptions,
  toPostgresLogicalRequestOptions,
  validatePostgresLogicalOptions,
  validatePostgresUri,
} from './postgres-logical.utils';

const draft = (overrides: Partial<ReturnType<typeof defaultPostgresLogicalOptions>> = {}) => ({
  ...defaultPostgresLogicalOptions(),
  ...overrides,
});

describe('validatePostgresLogicalOptions', () => {
  it('accepts the defaults and valid filters', () => {
    expect(validatePostgresLogicalOptions(draft())).toEqual({});
    expect(
      validatePostgresLogicalOptions(
        draft({ database: 'shop', excludeSchemas: ['scratch'], excludeTables: ['sales.audit_log'] }),
      ),
    ).toEqual({});
  });

  it('rejects names the backend would reject', () => {
    const errors = validatePostgresLogicalOptions(
      draft({
        database: 'shop;drop',
        includeSchemas: ['public', 'openeverest_migration'],
        excludeTables: ['audit_log', 'sales.audit_log'],
      }),
    );
    expect(Object.keys(errors).sort()).toEqual(['database', 'excludeTables', 'includeSchemas']);
    expect(errors.includeSchemas).toMatch(/reserved/);
    expect(errors.excludeTables).toMatch(/audit_log/);
  });

  it('does not allow include and exclude schemas together', () => {
    expect(
      validatePostgresLogicalOptions(draft({ includeSchemas: ['a'], excludeSchemas: ['b'] })).excludeSchemas,
    ).toMatch(/either include or exclude/);
  });
});

describe('toPostgresLogicalRequestOptions', () => {
  it('omits an empty database and trims it otherwise', () => {
    expect(toPostgresLogicalRequestOptions(draft())).toEqual({
      includeSchemas: [],
      excludeSchemas: [],
      excludeTables: [],
      recreateDatabase: false,
    });
    expect(toPostgresLogicalRequestOptions(draft({ database: ' shop ', recreateDatabase: true }))).toMatchObject({
      database: 'shop',
      recreateDatabase: true,
    });
  });
});

describe('summarizePostgresLogicalOptions', () => {
  it('describes a stored snapshot', () => {
    const summary = summarizePostgresLogicalOptions({ database: 'shop', excludeSchemas: ['scratch'] });
    expect(Object.fromEntries(summary.map(({ label, value }) => [label, value]))).toEqual({
      Database: 'shop',
      'Include schemas': 'All',
      'Exclude schemas': 'scratch',
      'Exclude tables': 'None',
      'Recreate target database': 'No',
    });
  });
});

describe('isPostgresLogicalOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isPostgresLogicalOptionsDraft(defaultPostgresLogicalOptions())).toBe(true);
    expect(isPostgresLogicalOptionsDraft({ ...defaultPostgresLogicalOptions(), excludeTables: [1] })).toBe(false);
    expect(isPostgresLogicalOptionsDraft({ includeNamespaces: [] })).toBe(false);
  });
});

describe('validatePostgresUri', () => {
  it('accepts single-host postgres URIs', () => {
    expect(validatePostgresUri('postgresql://user:p@ss@db1:5432/shop?sslmode=require')).toBeUndefined();
    expect(validatePostgresUri('postgres://db1/shop')).toBeUndefined();
  });

  it('rejects other schemes, missing hosts and multi-host URIs', () => {
    expect(validatePostgresUri('')).toMatch(/Enter/);
    expect(validatePostgresUri('mysql://db1/shop')).toMatch(/postgres:\/\//);
    expect(validatePostgresUri('postgresql://user@/shop')).toMatch(/host/);
    expect(validatePostgresUri('postgresql://db1:5432,db2:5432/shop')).toMatch(/single host/);
  });
});
