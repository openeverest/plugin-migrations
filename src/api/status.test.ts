import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { initHostApi, jsonResponse } from '../__mocks__/host-api';
import { fetchInstances } from './instances';
import { fetchStatus } from './status';

const fetchMock = vi.fn<PluginApi['fetch']>();

describe('status and instances API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    initHostApi(fetchMock);
  });

  it('parses engines from /api/status and skips entries without an id', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 'ready',
        storage: 'kubernetes',
        engines: [
          {
            id: 'pcsm',
            displayName: 'Percona ClusterSync for MongoDB',
            sourceDatabaseTypes: ['mongodb'],
            targetDatabaseTypes: ['mongodb'],
            targetProviders: ['percona-server-mongodb'],
          },
          { displayName: 'nameless' },
        ],
      }),
    );
    const status = await fetchStatus();
    expect(status.engines).toEqual([
      {
        id: 'pcsm',
        displayName: 'Percona ClusterSync for MongoDB',
        sourceDatabaseTypes: ['mongodb'],
        targetDatabaseTypes: ['mongodb'],
        targetProviders: ['percona-server-mongodb'],
      },
    ]);
  });

  it('rejects a status body without an engines list', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 'ready' }));
    await expect(fetchStatus()).rejects.toMatchObject({ message: expect.stringMatching(/unexpected response/) });
  });

  it('queries instances by cluster and drops malformed entries', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        instances: [
          { k8sCluster: 'main', namespace: 'shop', name: 'orders', provider: 'percona-server-mongodb' },
          { k8sCluster: 'main', name: 'no-namespace', provider: 'x' },
        ],
      }),
    );
    const instances = await fetchInstances('eu west');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/instances?k8sCluster=eu+west');
    expect(instances).toEqual([{ k8sCluster: 'main', namespace: 'shop', name: 'orders', provider: 'percona-server-mongodb' }]);
  });
});
