import { describe, expect, it } from 'vitest';
import { clusterFromBasePath } from './cluster.utils';

describe('clusterFromBasePath', () => {
  it('extracts and decodes the cluster segment', () => {
    expect(clusterFromBasePath('/v1/clusters/main/plugins/plugin-migrations')).toBe('main');
    expect(clusterFromBasePath('/v1/clusters/eu%20west/plugins/plugin-migrations/')).toBe('eu west');
  });

  it('rejects paths that are not plugin base paths', () => {
    expect(clusterFromBasePath('/v1/plugins/plugin-migrations')).toBeUndefined();
    expect(clusterFromBasePath('/v1/clusters/%E0%A4%A/plugins/x')).toBeUndefined();
    expect(clusterFromBasePath('/v1/clusters/main/plugins/x/extra')).toBeUndefined();
  });
});
