import type { EndpointRef } from '../api/migrations.types';
import type { InstanceSummary } from '../api/instances.types';
import { Messages } from './endpoint.messages';

export function endpointLabel(endpoint: EndpointRef): string {
  if (endpoint.type === 'external') return endpoint.display || Messages.externalSource;
  return `${endpoint.namespace}/${endpoint.name}`;
}

export function endpointTypeLabel(endpoint: EndpointRef): string {
  return endpoint.type === 'external' ? Messages.external : Messages.instance;
}

export function instanceKey(instance: Pick<InstanceSummary, 'k8sCluster' | 'namespace' | 'name'>): string {
  return `${instance.k8sCluster}/${instance.namespace}/${instance.name}`;
}

export function instanceLabel(instance: InstanceSummary): string {
  return `${instance.namespace}/${instance.name}`;
}
