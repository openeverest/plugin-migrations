import { describe, expect, it } from 'vitest';
import { subPathFromPathname, urlForView, viewFromSubPath } from './plugin-navigation.utils';

describe('plugin navigation', () => {
  it('maps sub-paths to views', () => {
    expect(viewFromSubPath(undefined)).toEqual({ kind: 'list' });
    expect(viewFromSubPath('')).toEqual({ kind: 'list' });
    expect(viewFromSubPath('/new')).toEqual({ kind: 'create' });
    expect(viewFromSubPath('mig-7f3k2p9q/')).toEqual({ kind: 'details', id: 'mig-7f3k2p9q' });
    expect(viewFromSubPath('mig%2F1')).toEqual({ kind: 'details', id: 'mig/1' });
  });

  it('builds host URLs that round-trip through the pathname', () => {
    const url = urlForView('plugin-migrations', { kind: 'details', id: 'mig/1' });
    expect(url).toBe('/plugins/plugin-migrations/mig%2F1');
    expect(viewFromSubPath(subPathFromPathname(url, 'plugin-migrations'))).toEqual({ kind: 'details', id: 'mig/1' });
    expect(urlForView('plugin-migrations', { kind: 'list' })).toBe('/plugins/plugin-migrations');
  });

  it('ignores pathnames outside the plugin route', () => {
    expect(subPathFromPathname('/databases', 'plugin-migrations')).toBeUndefined();
    expect(subPathFromPathname('/plugins/plugin-migrations-other/x', 'plugin-migrations')).toBeUndefined();
  });
});
