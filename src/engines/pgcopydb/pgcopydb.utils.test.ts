import { describe, expect, it } from 'vitest';
import {
  defaultPgcopydbOptions,
  isPgcopydbOptionsDraft,
  summarizePgcopydbOptions,
  toPgcopydbRequestOptions,
  validatePgcopydbOptions,
  validatePostgresUri,
} from './pgcopydb.utils';

const draft = (overrides: Partial<ReturnType<typeof defaultPgcopydbOptions>> = {}) => ({
  ...defaultPgcopydbOptions(),
  ...overrides,
});

describe('validatePgcopydbOptions', () => {
  it('accepts the defaults and valid filters', () => {
    expect(validatePgcopydbOptions(draft())).toEqual({});
    expect(
      validatePgcopydbOptions(
        draft({ database: 'shop', excludeSchemas: ['scratch'], excludeTables: ['sales.audit_log'] }),
      ),
    ).toEqual({});
  });

  it('rejects names the backend would reject', () => {
    const errors = validatePgcopydbOptions(
      draft({
        database: 'shop;drop',
        includeSchemas: ['public', 'pgcopydb'],
        excludeTables: ['audit_log', 'sales.audit_log'],
        tuning: { tableJobs: '0', indexJobs: '8' },
      }),
    );
    expect(Object.keys(errors).sort()).toEqual(['database', 'excludeTables', 'includeSchemas', 'tableJobs']);
    expect(errors.includeSchemas).toMatch(/reserved/);
    expect(errors.excludeTables).toMatch(/audit_log/);
  });

  it('does not allow include and exclude schemas together', () => {
    expect(validatePgcopydbOptions(draft({ includeSchemas: ['a'], excludeSchemas: ['b'] })).excludeSchemas).toMatch(
      /either include or exclude/,
    );
  });
});

describe('toPgcopydbRequestOptions', () => {
  it('omits empty optional fields and converts tuning to numbers', () => {
    expect(toPgcopydbRequestOptions(draft())).toEqual({
      includeSchemas: [],
      excludeSchemas: [],
      excludeTables: [],
      dropIfExists: false,
    });
    expect(toPgcopydbRequestOptions(draft({ database: ' shop ', tuning: { tableJobs: '8', indexJobs: '' } }))).toEqual({
      database: 'shop',
      includeSchemas: [],
      excludeSchemas: [],
      excludeTables: [],
      dropIfExists: false,
      tableJobs: 8,
    });
  });
});

describe('summarizePgcopydbOptions', () => {
  it('describes a stored snapshot', () => {
    const summary = summarizePgcopydbOptions({ database: 'shop', excludeSchemas: ['scratch'], tableJobs: 8 });
    expect(Object.fromEntries(summary.map(({ label, value }) => [label, value]))).toMatchObject({
      Database: 'shop',
      'Include schemas': 'All',
      'Exclude schemas': 'scratch',
      'Replace existing objects': 'No',
      'Table jobs': '8',
      'Index jobs': 'pgcopydb default',
    });
  });
});

describe('isPgcopydbOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isPgcopydbOptionsDraft(defaultPgcopydbOptions())).toBe(true);
    expect(isPgcopydbOptionsDraft({ ...defaultPgcopydbOptions(), excludeTables: [1] })).toBe(false);
    expect(isPgcopydbOptionsDraft({ includeNamespaces: [] })).toBe(false);
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
