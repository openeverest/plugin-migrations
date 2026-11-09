import { describe, expect, it } from 'vitest';
import {
  defaultRedisShakeOptions,
  isRedisShakeOptionsDraft,
  summarizeRedisShakeOptions,
  validateRedisShakeOptions,
  validateRedisUri,
} from './redis-shake.utils';

describe('validateRedisShakeOptions', () => {
  it('accepts the defaults and ordinary prefixes', () => {
    expect(validateRedisShakeOptions(defaultRedisShakeOptions())).toEqual({});
    expect(validateRedisShakeOptions({ flushTarget: true, excludeKeyPrefixes: ['cache:', 'tmp/'] })).toEqual({});
  });

  it('rejects prefixes the worker would reject', () => {
    for (const prefix of ['__open', 'a\u0007', 'x'.repeat(257)]) {
      expect(validateRedisShakeOptions({ flushTarget: false, excludeKeyPrefixes: [prefix] }).excludeKeyPrefixes).toBeDefined();
    }
    const tooMany = Array.from({ length: 65 }, (_, index) => `p${index}:`);
    expect(validateRedisShakeOptions({ flushTarget: false, excludeKeyPrefixes: tooMany }).excludeKeyPrefixes).toMatch(/at most 64/);
  });
});

describe('isRedisShakeOptionsDraft', () => {
  it('recognizes only complete drafts', () => {
    expect(isRedisShakeOptionsDraft(defaultRedisShakeOptions())).toBe(true);
    expect(isRedisShakeOptionsDraft({ flushTarget: 'yes', excludeKeyPrefixes: [] })).toBe(false);
    expect(isRedisShakeOptionsDraft({ databases: [] })).toBe(false);
  });
});

describe('summarizeRedisShakeOptions', () => {
  it('describes a stored snapshot', () => {
    expect(summarizeRedisShakeOptions({ flushTarget: true, excludeKeyPrefixes: ['cache:'] }).map((item) => item.value)).toEqual([
      'cache:',
      'Yes',
    ]);
    expect(summarizeRedisShakeOptions(undefined).map((item) => item.value)).toEqual(['None', 'No']);
  });
});

describe('validateRedisUri', () => {
  it('accepts single-host Redis and Valkey URIs', () => {
    for (const uri of ['redis://:pw@db1:6379', 'rediss://default:pw@db1', 'VALKEYS://db1/0']) {
      expect(validateRedisUri(uri)).toBeUndefined();
    }
  });

  it('rejects other schemes and multi-host URIs', () => {
    expect(validateRedisUri('mysql://db1')).toBe('The URI must start with redis://, rediss://, valkey:// or valkeys://.');
    expect(validateRedisUri('redis://a:1,b:2')).toMatch(/single host/);
  });
});
