import { describe, expect, it } from 'vitest';
import {
  defaultMssqlLogShippingOptions,
  isMssqlLogShippingOptionsDraft,
  summarizeMssqlLogShippingOptions,
  toMssqlLogShippingRequestOptions,
  validateMssqlLogShippingOptions,
  validateSqlServerUri,
} from './mssql-log-shipping.utils';

describe('validateMssqlLogShippingOptions', () => {
  it('accepts the defaults and ordinary names', () => {
    expect(validateMssqlLogShippingOptions(defaultMssqlLogShippingOptions())).toEqual({});
    expect(
      validateMssqlLogShippingOptions({
        ...defaultMssqlLogShippingOptions(),
        databases: ['shop', 'Sales_2024'],
        logBackupIntervalSeconds: '30',
      }),
    ).toEqual({});
  });

  it('rejects names and intervals the worker would reject', () => {
    const draft = defaultMssqlLogShippingOptions();
    for (const name of ['msdb', 'TempDB', 'a]b', '-x', 'a b']) {
      expect(validateMssqlLogShippingOptions({ ...draft, databases: [name] }).databases).toBeDefined();
    }
    for (const interval of ['4', '301', '1.5', 'ten']) {
      expect(
        validateMssqlLogShippingOptions({ ...draft, logBackupIntervalSeconds: interval }).logBackupIntervalSeconds,
      ).toBeDefined();
    }
  });
});

describe('toMssqlLogShippingRequestOptions', () => {
  it('omits an empty interval and keeps the switches', () => {
    expect(toMssqlLogShippingRequestOptions({ ...defaultMssqlLogShippingOptions(), databases: ['shop'] })).toEqual({
      databases: ['shop'],
      recreateDatabases: false,
      copyLogins: true,
      leaveSourceRestoring: false,
      ignoreRecentLogBackups: false,
    });
    expect(
      toMssqlLogShippingRequestOptions({ ...defaultMssqlLogShippingOptions(), logBackupIntervalSeconds: ' 20 ' })
        .logBackupIntervalSeconds,
    ).toBe(20);
  });
});

describe('isMssqlLogShippingOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isMssqlLogShippingOptionsDraft(defaultMssqlLogShippingOptions())).toBe(true);
    expect(isMssqlLogShippingOptionsDraft({ databases: [], excludeTables: [] })).toBe(false);
  });
});

describe('summarizeMssqlLogShippingOptions', () => {
  it('describes a snapshot from the API', () => {
    const summary = summarizeMssqlLogShippingOptions({
      databases: ['shop'],
      copyLogins: false,
      logBackupIntervalSeconds: 30,
    });
    expect(summary.map((item) => item.value)).toEqual(['shop', 'No', 'No', 'No', 'No', '30 s']);
    expect(summarizeMssqlLogShippingOptions({})[0].value).toBe('From the connection URI');
    expect(summarizeMssqlLogShippingOptions({})[2].value).toBe('Yes');
  });
});

describe('validateSqlServerUri', () => {
  it('requires a single-host sqlserver:// URI', () => {
    expect(validateSqlServerUri('sqlserver://sa:pw@db1:1433?database=shop')).toBeUndefined();
    expect(validateSqlServerUri('mssql://db1')).toBeDefined();
    expect(validateSqlServerUri('sqlserver://a,b')).toBeDefined();
  });
});
