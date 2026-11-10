import type { InstanceSummary } from '../../api/instances.types';
import type { Engine } from '../../engines/engine.types';

export type WizardStep = 'engine' | 'source' | 'target' | 'options' | 'review';

export type SourceKind = 'instance' | 'external';

export interface CreateMigrationDraft {
  name: string;
  engineId: string;
  sourceKind: SourceKind;
  sourceInstanceKey: string;
  externalUri: string;
  caCert: string;
  targetInstanceKey: string;
  /** For engines that require one: a BackupStorage in the target's namespace. */
  backupStorageName: string;
  /** Engine-specific form state, owned by the selected engine. */
  options: unknown;
}

export interface WizardContext {
  engine: Engine | undefined;
  sourceInstances: InstanceSummary[];
  targetInstances: InstanceSummary[];
}

export type DraftErrors = Readonly<Partial<Record<'name' | 'engine' | 'sourceInstance' | 'externalUri' | 'targetInstance' | 'backupStorage' | 'options', string>>>;
