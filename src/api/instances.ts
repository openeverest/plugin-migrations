import { apiFetch } from '../plugin-api';
import { isRecord, readString } from '../utils/guards.utils';
import type { InstanceSummary } from './instances.types';

function parseInstance(value: unknown): InstanceSummary | undefined {
  if (!isRecord(value)) return undefined;
  const k8sCluster = readString(value, 'k8sCluster');
  const namespace = readString(value, 'namespace');
  const name = readString(value, 'name');
  const provider = readString(value, 'provider');
  if (!k8sCluster || !namespace || !name || provider === undefined) return undefined;
  return { k8sCluster, namespace, name, provider };
}

function parseInstances(body: unknown): InstanceSummary[] | undefined {
  if (!isRecord(body) || !Array.isArray(body.instances)) return undefined;
  return body.instances
    .map(parseInstance)
    .filter((instance): instance is InstanceSummary => instance !== undefined);
}

export function fetchInstances(k8sCluster: string, signal?: AbortSignal): Promise<InstanceSummary[]> {
  const query = new URLSearchParams({ k8sCluster });
  return apiFetch(`/instances?${query}`, { method: 'GET', signal }, parseInstances);
}
