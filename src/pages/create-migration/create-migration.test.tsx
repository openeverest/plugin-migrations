import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { CreateMigration } from './create-migration';

const mocks = vi.hoisted(() => {
  const status = {
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
    ],
  };
  const instances = [{ k8sCluster: 'main', namespace: 'shop', name: 'orders', provider: 'percona-server-mongodb' }];
  return {
    fetchStatus: vi.fn(() => Promise.resolve(status)),
    fetchInstances: vi.fn(() => Promise.resolve(instances)),
  };
});

vi.mock('../../api/status', () => ({ fetchStatus: mocks.fetchStatus }));
vi.mock('../../api/instances', () => ({ fetchInstances: mocks.fetchInstances }));

const isDisabled = (element: HTMLElement) => element.hasAttribute('disabled');

describe('CreateMigration wizard', () => {
  it('starts with a valid default name and blocks Next on invalid names and URIs', async () => {
    render(<CreateMigration k8sCluster="main" onNavigate={vi.fn()} />);

    const name = await screen.findByRole<HTMLInputElement>('textbox', { name: /name/i });
    const next = screen.getByRole('button', { name: 'Next' });
    expect(mocks.fetchInstances).toHaveBeenCalledWith('main', expect.any(AbortSignal));
    expect(name.value).toMatch(/^migration-[a-z0-9]{5}$/);
    expect(isDisabled(next)).toBe(false);

    fireEvent.change(name, { target: { value: '' } });
    expect(isDisabled(next)).toBe(true);

    fireEvent.change(name, { target: { value: 'Orders_DB' } });
    expect(screen.getByText(/Use lowercase letters/)).toBeTruthy();
    expect(isDisabled(next)).toBe(true);

    fireEvent.change(name, { target: { value: 'orders-db' } });
    expect(isDisabled(next)).toBe(false);
    fireEvent.click(next);

    const uri = screen.getByRole('textbox', { name: /connection uri/i });
    expect(isDisabled(screen.getByRole('button', { name: 'Next' }))).toBe(true);

    fireEvent.change(uri, { target: { value: 'http://db1:27017' } });
    expect(screen.getByText(/must start with mongodb:\/\//)).toBeTruthy();
    expect(isDisabled(screen.getByRole('button', { name: 'Next' }))).toBe(true);

    fireEvent.change(uri, { target: { value: 'mongodb://db1:27017' } });
    expect(isDisabled(screen.getByRole('button', { name: 'Next' }))).toBe(false);
  });
});
