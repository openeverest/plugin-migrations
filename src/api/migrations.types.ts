export type MigrationPhase =
  | 'provisioning'
  | 'starting'
  | 'full-load'
  | 'catching-up'
  | 'replicating'
  | 'paused'
  | 'finalizing'
  | 'completed'
  | 'failed'
  | 'unknown';

export type MigrationAction = 'pause' | 'resume' | 'cutover' | 'delete';

export type WorkerAction = Exclude<MigrationAction, 'delete'>;

export interface InstanceEndpointRef {
  type: 'instance';
  k8sCluster: string;
  namespace: string;
  name: string;
  provider?: string;
}

export interface ExternalEndpointRef {
  type: 'external';
  display: string;
}

export type EndpointRef = InstanceEndpointRef | ExternalEndpointRef;

export interface MigrationProgress {
  bytesTotal: number;
  bytesCopied: number;
  lagSeconds: number;
  eventsApplied: number;
  lastReplicatedAt?: string;
  readyForCutover: boolean;
}

export interface Migration {
  id: string;
  name: string;
  engine: string;
  source: EndpointRef;
  target: EndpointRef;
  options: Record<string, unknown>;
  createdAt: string;
  startedAt?: string;
  phase: MigrationPhase;
  message: string;
  error: string;
  progress: MigrationProgress;
  observedAt?: string;
  allowedActions: MigrationAction[];
}

export interface InstanceEndpointInput {
  type: 'instance';
  k8sCluster: string;
  namespace: string;
  name: string;
}

export interface ExternalEndpointInput {
  type: 'external';
  uri: string;
  caCert: string;
}

export type SourceEndpointInput = InstanceEndpointInput | ExternalEndpointInput;

export interface CreateMigrationRequest {
  name: string;
  engine: string;
  source: SourceEndpointInput;
  target: InstanceEndpointInput;
  options: Record<string, unknown>;
}
