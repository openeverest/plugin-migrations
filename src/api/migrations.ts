import { apiFetch, apiFetchNoContent, jsonRequest } from '../plugin-api';
import { parseMigration, parseMigrationList } from './migrations.utils';
import type { CreateMigrationRequest, Migration, WorkerAction } from './migrations.types';

function migrationPath(id: string): string {
  return `/migrations/${encodeURIComponent(id)}`;
}

export function listMigrations(signal?: AbortSignal): Promise<Migration[]> {
  return apiFetch('/migrations', { method: 'GET', signal }, parseMigrationList);
}

export function getMigration(id: string, signal?: AbortSignal): Promise<Migration> {
  return apiFetch(migrationPath(id), { method: 'GET', signal }, parseMigration);
}

export function createMigration(request: CreateMigrationRequest, signal?: AbortSignal): Promise<Migration> {
  return apiFetch('/migrations', jsonRequest('POST', request, signal), parseMigration);
}

function runWorkerAction(id: string, action: WorkerAction): Promise<Migration> {
  return apiFetch(`${migrationPath(id)}/${action}`, { method: 'POST' }, parseMigration);
}

export function pauseMigration(id: string): Promise<Migration> {
  return runWorkerAction(id, 'pause');
}

export function resumeMigration(id: string): Promise<Migration> {
  return runWorkerAction(id, 'resume');
}

export function cutoverMigration(id: string): Promise<Migration> {
  return runWorkerAction(id, 'cutover');
}

export function deleteMigration(id: string): Promise<void> {
  return apiFetchNoContent(migrationPath(id), { method: 'DELETE' });
}
