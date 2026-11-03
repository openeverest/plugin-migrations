import { describe, expect, it } from 'vitest';
import { maskUriCredentials } from './uri.utils';

describe('maskUriCredentials', () => {
  it('masks the password and keeps the user and hosts', () => {
    expect(maskUriCredentials('mongodb://admin:s3cret@db1:27017,db2:27017/?replicaSet=rs0')).toBe(
      'mongodb://admin:****@db1:27017,db2:27017/?replicaSet=rs0',
    );
    expect(maskUriCredentials('mongodb+srv://app:p%40ss@cluster.example.com/db')).toBe(
      'mongodb+srv://app:****@cluster.example.com/db',
    );
  });

  it('never leaks a password that contains unencoded special characters', () => {
    const masked = maskUriCredentials('mongodb://user:pa:ss@w/rd@db1:27017/');
    expect(masked).toBe('mongodb://user:****@db1:27017/');
    expect(masked).not.toContain('pa:ss');
    expect(masked).not.toContain('rd');
  });

  it('leaves URIs without credentials untouched and keeps a user without a password', () => {
    expect(maskUriCredentials('mongodb://db1:27017')).toBe('mongodb://db1:27017');
    expect(maskUriCredentials('mongodb://user@db1:27017')).toBe('mongodb://user@db1:27017');
  });
});
