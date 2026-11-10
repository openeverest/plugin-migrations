import { apiFetch } from '../plugin-api';
import { isRecord, readString } from '../utils/guards.utils';
import type { BackupStorageSummary } from './backup-storages.types';

function parseStorage(value: unknown): BackupStorageSummary | undefined {
  if (!isRecord(value)) return undefined;
  const name = readString(value, 'name');
  if (!name) return undefined;
  return {
    name,
    type: readString(value, 'type') ?? '',
    bucket: readString(value, 'bucket') ?? '',
    endpointURL: readString(value, 'endpointURL') ?? '',
  };
}

export function parseBackupStorages(body: unknown): BackupStorageSummary[] | undefined {
  if (!isRecord(body) || !Array.isArray(body.backupStorages)) return undefined;
  return body.backupStorages
    .map(parseStorage)
    .filter((storage): storage is BackupStorageSummary => storage !== undefined);
}

/** Backup storages the user can read in a namespace; never includes credentials. */
export function fetchBackupStorages(
  k8sCluster: string,
  namespace: string,
  signal?: AbortSignal,
): Promise<BackupStorageSummary[]> {
  const query = new URLSearchParams({ k8sCluster, namespace });
  return apiFetch(`/backup-storages?${query}`, { method: 'GET', signal }, parseBackupStorages);
}
