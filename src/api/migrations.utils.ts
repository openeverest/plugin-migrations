import { isRecord, readBoolean, readNumber, readRecord, readString } from '../utils/guards.utils';
import { isMigrationAction, isMigrationPhase } from './migrations.constants';
import type { EndpointRef, Migration, MigrationAction, MigrationProgress } from './migrations.types';

export function parseEndpointRef(value: unknown): EndpointRef | undefined {
  if (!isRecord(value)) return undefined;
  const type = readString(value, 'type');
  if (type === 'external') {
    return { type, display: readString(value, 'display') ?? '' };
  }
  if (type === 'instance') {
    const k8sCluster = readString(value, 'k8sCluster');
    const namespace = readString(value, 'namespace');
    const name = readString(value, 'name');
    if (k8sCluster === undefined || !namespace || !name) return undefined;
    const provider = readString(value, 'provider');
    return provider ? { type, k8sCluster, namespace, name, provider } : { type, k8sCluster, namespace, name };
  }
  return undefined;
}

function parseProgress(value: Record<string, unknown> | undefined): MigrationProgress {
  const progress = value ?? {};
  const lastReplicatedAt = readString(progress, 'lastReplicatedAt');
  return {
    bytesTotal: readNumber(progress, 'bytesTotal') ?? 0,
    bytesCopied: readNumber(progress, 'bytesCopied') ?? 0,
    lagSeconds: readNumber(progress, 'lagSeconds') ?? 0,
    eventsApplied: readNumber(progress, 'eventsApplied') ?? 0,
    ...(lastReplicatedAt ? { lastReplicatedAt } : {}),
    readyForCutover: readBoolean(progress, 'readyForCutover') ?? false,
  };
}

function parseAllowedActions(value: unknown): MigrationAction[] {
  if (!Array.isArray(value)) return [];
  return value.filter(isMigrationAction);
}

export function parseMigration(value: unknown): Migration | undefined {
  if (!isRecord(value)) return undefined;
  const id = readString(value, 'id');
  const name = readString(value, 'name');
  const engine = readString(value, 'engine');
  const createdAt = readString(value, 'createdAt');
  const source = parseEndpointRef(value.source);
  const target = parseEndpointRef(value.target);
  if (!id || !name || !engine || createdAt === undefined || !source || !target) return undefined;
  const startedAt = readString(value, 'startedAt');
  const observedAt = readString(value, 'observedAt');
  return {
    id,
    name,
    engine,
    source,
    target,
    options: readRecord(value, 'options') ?? {},
    createdAt,
    ...(startedAt ? { startedAt } : {}),
    // A phase newer than this UI is shown as unknown rather than dropping the migration.
    phase: isMigrationPhase(value.phase) ? value.phase : 'unknown',
    message: readString(value, 'message') ?? '',
    error: readString(value, 'error') ?? '',
    progress: parseProgress(readRecord(value, 'progress')),
    ...(observedAt ? { observedAt } : {}),
    allowedActions: parseAllowedActions(value.allowedActions),
  };
}

export function parseMigrationList(body: unknown): Migration[] | undefined {
  if (!isRecord(body) || !Array.isArray(body.migrations)) return undefined;
  return body.migrations
    .map(parseMigration)
    .filter((migration): migration is Migration => migration !== undefined);
}
