import { apiFetch } from '../plugin-api';
import { isRecord, readString, readStringArray } from '../utils/guards.utils';
import type { EngineDescriptor, PluginStatus } from './status.types';

function parseEngineDescriptor(value: unknown): EngineDescriptor | undefined {
  if (!isRecord(value)) return undefined;
  const id = readString(value, 'id');
  if (!id) return undefined;
  return {
    id,
    displayName: readString(value, 'displayName') || id,
    sourceDatabaseTypes: readStringArray(value, 'sourceDatabaseTypes') ?? [],
    targetDatabaseTypes: readStringArray(value, 'targetDatabaseTypes') ?? [],
    targetProviders: readStringArray(value, 'targetProviders') ?? [],
  };
}

function parseStatus(body: unknown): PluginStatus | undefined {
  if (!isRecord(body) || !Array.isArray(body.engines)) return undefined;
  return {
    status: readString(body, 'status') ?? '',
    storage: readString(body, 'storage') ?? '',
    engines: body.engines
      .map(parseEngineDescriptor)
      .filter((engine): engine is EngineDescriptor => engine !== undefined),
  };
}

export function fetchStatus(signal?: AbortSignal): Promise<PluginStatus> {
  return apiFetch('/status', { method: 'GET', signal }, parseStatus);
}
