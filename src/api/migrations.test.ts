import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { initHostApi, jsonResponse } from '../__mocks__/host-api';
import { ApiError } from '../plugin-api';
import { createMigration, deleteMigration, getMigration, listMigrations, pauseMigration } from './migrations';
import type { CreateMigrationRequest } from './migrations.types';

const fetchMock = vi.fn<PluginApi['fetch']>();

const CONTRACT_MIGRATION = {
  id: 'mig-7f3k2p9q',
  name: 'orders-into-everest',
  engine: 'pcsm',
  source: { type: 'external', display: 'db1:27017,db2:27017' },
  target: { type: 'instance', k8sCluster: 'main', namespace: 'shop', name: 'orders', provider: 'percona-server-mongodb' },
  options: { includeNamespaces: ['shop.*'], excludeNamespaces: [], pauseOnInitialSync: false, clone: {}, replication: {} },
  createdAt: '2026-11-03T10:00:00Z',
  startedAt: '2026-11-03T10:00:20Z',
  phase: 'full-load',
  message: 'Initial Sync: Cloning Data',
  error: '',
  progress: {
    bytesTotal: 1073741824,
    bytesCopied: 268435456,
    lagSeconds: 0,
    eventsApplied: 0,
    lastReplicatedAt: '',
    readyForCutover: false,
  },
  observedAt: '2026-11-03T10:05:00Z',
  allowedActions: ['delete'],
};

describe('migrations API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    initHostApi(fetchMock);
  });

  it('parses the contract Migration shape', async () => {
    fetchMock.mockResolvedValue(jsonResponse(CONTRACT_MIGRATION));
    const migration = await getMigration('mig-7f3k2p9q');
    expect(fetchMock).toHaveBeenCalledWith('/api/migrations/mig-7f3k2p9q', expect.objectContaining({ method: 'GET' }));
    expect(migration).toMatchObject({
      id: 'mig-7f3k2p9q',
      phase: 'full-load',
      source: { type: 'external', display: 'db1:27017,db2:27017' },
      target: { type: 'instance', namespace: 'shop', name: 'orders' },
      progress: { bytesCopied: 268435456, readyForCutover: false },
      allowedActions: ['delete'],
    });
    expect(migration.progress.lastReplicatedAt).toBeUndefined();
  });

  it('tolerates omitted optional fields and unknown phases/actions', async () => {
    const { startedAt: _s, observedAt: _o, progress: _p, ...minimal } = CONTRACT_MIGRATION;
    fetchMock.mockResolvedValue(
      jsonResponse({ migrations: [{ ...minimal, phase: 'rebalancing', allowedActions: ['delete', 'explode'] }] }),
    );
    const [migration] = await listMigrations();
    expect(migration.phase).toBe('unknown');
    expect(migration.allowedActions).toEqual(['delete']);
    expect(migration.progress).toEqual({
      bytesTotal: 0,
      bytesCopied: 0,
      lagSeconds: 0,
      eventsApplied: 0,
      readyForCutover: false,
    });
    expect(migration.startedAt).toBeUndefined();
  });

  it('drops malformed list items and rejects a malformed list', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ migrations: [CONTRACT_MIGRATION, { id: 'broken' }, { ...CONTRACT_MIGRATION, source: { type: 'ftp' } }] }),
    );
    await expect(listMigrations()).resolves.toHaveLength(1);

    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));
    await expect(listMigrations()).rejects.toBeInstanceOf(ApiError);
  });

  it('rejects a single migration without required fields', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ id: 'mig-1' }));
    await expect(getMigration('mig-1')).rejects.toBeInstanceOf(ApiError);
  });

  it('posts the create request as JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse(CONTRACT_MIGRATION, 201));
    const request: CreateMigrationRequest = {
      name: 'orders-into-everest',
      engine: 'pcsm',
      source: { type: 'external', uri: 'mongodb://u:p@db1:27017', caCert: '' },
      target: { type: 'instance', k8sCluster: 'main', namespace: 'shop', name: 'orders' },
      options: { includeNamespaces: ['shop.*'], excludeNamespaces: [], pauseOnInitialSync: false },
    };
    await createMigration(request);
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe('/api/migrations');
    expect(init?.method).toBe('POST');
    expect(typeof init?.body === 'string' ? JSON.parse(init.body) : undefined).toEqual(request);
  });

  it('surfaces 409 action conflicts with the backend message', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'action "pause" is not allowed in phase "full-load"' }, 409));
    await expect(pauseMigration('mig/1')).rejects.toMatchObject({
      status: 409,
      message: 'action "pause" is not allowed in phase "full-load"',
    });
    expect(fetchMock).toHaveBeenCalledWith('/api/migrations/mig%2F1/pause', { method: 'POST' });
  });

  it('deletes with a 204 response', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(deleteMigration('mig-1')).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith('/api/migrations/mig-1', { method: 'DELETE' });
  });
});
