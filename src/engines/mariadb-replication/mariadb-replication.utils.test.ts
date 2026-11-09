import { describe, expect, it } from 'vitest';
import { validateMariadbUri } from './mariadb-replication.utils';

describe('validateMariadbUri', () => {
  it('accepts mariadb:// and mysql:// single-host URIs', () => {
    expect(validateMariadbUri('mariadb://repl:pw@db1:3306/shop?ssl-mode=REQUIRED')).toBeUndefined();
    expect(validateMariadbUri('MySQL://repl:pw@db1/shop')).toBeUndefined();
  });

  it('names both schemes when another one is used', () => {
    expect(validateMariadbUri('postgres://db1/shop')).toBe('The URI must start with mariadb:// or mysql://.');
    expect(validateMariadbUri('mariadb://db1,db2/shop')).toMatch(/single host/);
  });
});
