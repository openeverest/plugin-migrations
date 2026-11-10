import type { InstanceSummary } from '../../api/instances.types';
import type { CreateMigrationRequest, SourceEndpointInput } from '../../api/migrations.types';
import type { Engine, OptionSummaryItem } from '../../engines/engine.types';
import { instanceKey, instanceLabel } from '../../utils/endpoint.utils';
import { maskUriCredentials } from '../../utils/uri.utils';
import {
  DEFAULT_NAME_ALPHABET,
  DEFAULT_NAME_PREFIX,
  DEFAULT_NAME_SUFFIX_LENGTH,
  NAME_MAX_LENGTH,
  NAME_PATTERN,
} from './create-migration.constants';
import { Messages } from './create-migration.messages';
import type { CreateMigrationDraft, DraftErrors, WizardContext, WizardStep } from './create-migration.types';

/** A random name such as "migration-k3x9a", so users can continue without inventing one. */
export function generateDefaultName(random: () => number = Math.random): string {
  const suffix = Array.from(
    { length: DEFAULT_NAME_SUFFIX_LENGTH },
    () => DEFAULT_NAME_ALPHABET[Math.floor(random() * DEFAULT_NAME_ALPHABET.length)],
  ).join('');
  return `${DEFAULT_NAME_PREFIX}-${suffix}`;
}

export function createInitialDraft(engines: readonly Engine[]): CreateMigrationDraft {
  const engine = engines.length === 1 ? engines[0] : undefined;
  return {
    name: generateDefaultName(),
    engineId: engine?.id ?? '',
    sourceKind: engine && !engine.externalSource ? 'instance' : 'external',
    sourceInstanceKey: '',
    externalUri: '',
    caCert: '',
    targetInstanceKey: '',
    backupStorageName: '',
    options: engine?.defaultOptions(),
  };
}

export function selectEngine(draft: CreateMigrationDraft, engine: Engine): CreateMigrationDraft {
  if (draft.engineId === engine.id) return draft;
  return {
    ...draft,
    engineId: engine.id,
    sourceKind: engine.externalSource ? draft.sourceKind : 'instance',
    sourceInstanceKey: '',
    targetInstanceKey: '',
    backupStorageName: '',
    options: engine.defaultOptions(),
  };
}

export function withSourceInstance(draft: CreateMigrationDraft, sourceInstanceKey: string): CreateMigrationDraft {
  if (draft.targetInstanceKey !== sourceInstanceKey) return { ...draft, sourceInstanceKey };
  return { ...draft, sourceInstanceKey, targetInstanceKey: '', backupStorageName: '' };
}

/** Backup storages live in the target's namespace, so a new target clears the choice. */
export function withTargetInstance(draft: CreateMigrationDraft, targetInstanceKey: string): CreateMigrationDraft {
  if (draft.targetInstanceKey === targetInstanceKey) return draft;
  return { ...draft, targetInstanceKey, backupStorageName: '' };
}

export function findTargetInstance(draft: CreateMigrationDraft, context: WizardContext): InstanceSummary | undefined {
  return findInstance(context.targetInstances, draft.targetInstanceKey);
}

export function validateName(name: string): string | undefined {
  if (name === '') return Messages.name.required;
  if (name.length > NAME_MAX_LENGTH) return Messages.name.tooLong(NAME_MAX_LENGTH);
  if (!NAME_PATTERN.test(name)) return Messages.name.invalid;
  return undefined;
}

export function filterSourceInstances(instances: readonly InstanceSummary[], engine: Engine | undefined): InstanceSummary[] {
  if (!engine) return [];
  return instances.filter((instance) => engine.sourceProviders.includes(instance.provider));
}

/** Compatible targets, excluding the instance chosen as the source. */
export function filterTargetInstances(
  instances: readonly InstanceSummary[],
  engine: Engine | undefined,
  excludedKey?: string,
): InstanceSummary[] {
  if (!engine) return [];
  return instances.filter(
    (instance) => engine.targetProviders.includes(instance.provider) && instanceKey(instance) !== excludedKey,
  );
}

export function buildWizardContext(
  draft: CreateMigrationDraft,
  engines: readonly Engine[],
  instances: readonly InstanceSummary[],
): WizardContext {
  const engine = engines.find((candidate) => candidate.id === draft.engineId);
  const excludedKey = draft.sourceKind === 'instance' ? draft.sourceInstanceKey : undefined;
  return {
    engine,
    sourceInstances: filterSourceInstances(instances, engine),
    targetInstances: filterTargetInstances(instances, engine, excludedKey),
  };
}

function findInstance(instances: readonly InstanceSummary[], key: string): InstanceSummary | undefined {
  return key === '' ? undefined : instances.find((instance) => instanceKey(instance) === key);
}

export function validateStep(step: WizardStep, draft: CreateMigrationDraft, context: WizardContext): DraftErrors {
  const { engine } = context;
  switch (step) {
    case 'engine': {
      const name = validateName(draft.name);
      return {
        ...(name ? { name } : {}),
        ...(engine ? {} : { engine: Messages.engine.required }),
      };
    }
    case 'source': {
      if (draft.sourceKind === 'external') {
        const validateUri = engine?.externalSource?.validateUri;
        const externalUri = validateUri ? validateUri(draft.externalUri) : Messages.source.required;
        return externalUri ? { externalUri } : {};
      }
      return findInstance(context.sourceInstances, draft.sourceInstanceKey)
        ? {}
        : { sourceInstance: Messages.source.required };
    }
    case 'target': {
      if (!findInstance(context.targetInstances, draft.targetInstanceKey)) {
        return { targetInstance: Messages.target.required };
      }
      return engine?.requiresBackupStorage && draft.backupStorageName === ''
        ? { backupStorage: Messages.target.backupStorageRequired }
        : {};
    }
    case 'options':
      return engine && Object.keys(engine.validateOptions(draft.options)).length === 0
        ? {}
        : { options: Messages.options.invalid };
    case 'review':
      return {};
  }
}

export function isStepValid(step: WizardStep, draft: CreateMigrationDraft, context: WizardContext): boolean {
  return Object.keys(validateStep(step, draft, context)).length === 0;
}

function sourceInput(draft: CreateMigrationDraft, context: WizardContext): SourceEndpointInput | undefined {
  if (draft.sourceKind === 'external') {
    return { type: 'external', uri: draft.externalUri.trim(), caCert: draft.caCert.trim() };
  }
  const source = findInstance(context.sourceInstances, draft.sourceInstanceKey);
  return source && { type: 'instance', k8sCluster: source.k8sCluster, namespace: source.namespace, name: source.name };
}

export function buildCreateRequest(
  draft: CreateMigrationDraft,
  context: WizardContext,
  steps: readonly WizardStep[],
): CreateMigrationRequest | undefined {
  const { engine } = context;
  if (!engine || !steps.every((step) => isStepValid(step, draft, context))) return undefined;
  const source = sourceInput(draft, context);
  const target = findInstance(context.targetInstances, draft.targetInstanceKey);
  if (!source || !target) return undefined;
  return {
    name: draft.name,
    engine: engine.id,
    source,
    target: { type: 'instance', k8sCluster: target.k8sCluster, namespace: target.namespace, name: target.name },
    options: engine.toRequestOptions(draft.options),
    ...(engine.requiresBackupStorage ? { backupStorage: { name: draft.backupStorageName } } : {}),
  };
}

export function describeSource(draft: CreateMigrationDraft, context: WizardContext): string {
  if (draft.sourceKind === 'external') return maskUriCredentials(draft.externalUri);
  const source = findInstance(context.sourceInstances, draft.sourceInstanceKey);
  return source ? instanceLabel(source) : '';
}

export function buildReviewSummary(draft: CreateMigrationDraft, context: WizardContext): OptionSummaryItem[] {
  const target = findInstance(context.targetInstances, draft.targetInstanceKey);
  return [
    { label: Messages.review.name, value: draft.name },
    { label: Messages.review.engine, value: context.engine?.label ?? draft.engineId },
    { label: Messages.review.source, value: describeSource(draft, context) },
    ...(draft.sourceKind === 'external'
      ? [
          {
            label: Messages.review.caCert,
            value: draft.caCert.trim() === '' ? Messages.review.caNotProvided : Messages.review.caProvided,
          },
        ]
      : []),
    { label: Messages.review.target, value: target ? instanceLabel(target) : '' },
    ...(context.engine?.requiresBackupStorage
      ? [{ label: Messages.review.backupStorage, value: draft.backupStorageName }]
      : []),
  ];
}

export function buildReviewOptions(draft: CreateMigrationDraft, engine: Engine | undefined): OptionSummaryItem[] {
  return engine ? engine.summarizeOptions(engine.toRequestOptions(draft.options)) : [];
}
