import { isRecord, readBoolean, readNumber, readRecord, readStringArray } from '../../utils/guards.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import { MONGODB_URI_SCHEMES, NAMESPACE_PATTERN, PCSM_TUNING_FIELDS } from './pcsm.constants';
import { Messages } from './pcsm.messages';
import type { PcsmOptions, PcsmOptionsDraft, PcsmTuningField, PcsmTuningFieldSpec } from './pcsm.types';

export function defaultPcsmOptions(): PcsmOptionsDraft {
  return {
    includeNamespaces: [],
    excludeNamespaces: [],
    pauseOnInitialSync: false,
    tuning: {
      cloneNumParallelCollections: '',
      cloneNumReadWorkers: '',
      cloneNumInsertWorkers: '',
      replicationNumWorkers: '',
    },
  };
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

export function isPcsmOptionsDraft(value: unknown): value is PcsmOptionsDraft {
  if (!isRecord(value) || !isRecord(value.tuning)) return false;
  const { tuning } = value;
  return (
    isStringArray(value.includeNamespaces) &&
    isStringArray(value.excludeNamespaces) &&
    typeof value.pauseOnInitialSync === 'boolean' &&
    PCSM_TUNING_FIELDS.every(({ field }) => typeof tuning[field] === 'string')
  );
}

export function isValidNamespace(namespace: string): boolean {
  return NAMESPACE_PATTERN.test(namespace);
}

function validateNamespaceList(namespaces: readonly string[]): string | undefined {
  const invalid = namespaces.filter((namespace) => !isValidNamespace(namespace));
  if (invalid.length > 0) return Messages.invalidNamespaces(invalid);
  const duplicates = namespaces.filter((namespace, index) => namespaces.indexOf(namespace) !== index);
  if (duplicates.length > 0) return Messages.duplicateNamespaces([...new Set(duplicates)]);
  return undefined;
}

function parseTuningValue(value: string): number | undefined {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

function validateTuningValue(value: string, { min, max }: PcsmTuningFieldSpec): string | undefined {
  if (value.trim() === '') return undefined;
  const parsed = parseTuningValue(value);
  if (parsed === undefined || parsed < min || parsed > max) return Messages.integerRange(min, max);
  return undefined;
}

export function validatePcsmOptions(draft: PcsmOptionsDraft): EngineOptionsErrors {
  const errors: Record<string, string> = {};
  const include = validateNamespaceList(draft.includeNamespaces);
  if (include) errors.includeNamespaces = include;
  const exclude = validateNamespaceList(draft.excludeNamespaces);
  if (exclude) errors.excludeNamespaces = exclude;
  for (const spec of PCSM_TUNING_FIELDS) {
    const error = validateTuningValue(draft.tuning[spec.field], spec);
    if (error) errors[spec.field] = error;
  }
  return errors;
}

export function toPcsmRequestOptions(draft: PcsmOptionsDraft): PcsmOptions {
  const groups: Record<PcsmTuningFieldSpec['group'], Record<string, number>> = { clone: {}, replication: {} };
  for (const spec of PCSM_TUNING_FIELDS) {
    const value = parseTuningValue(draft.tuning[spec.field]);
    if (value !== undefined) groups[spec.group][spec.key] = value;
  }
  return {
    includeNamespaces: [...draft.includeNamespaces],
    excludeNamespaces: [...draft.excludeNamespaces],
    pauseOnInitialSync: draft.pauseOnInitialSync,
    ...(Object.keys(groups.clone).length > 0 ? { clone: groups.clone } : {}),
    ...(Object.keys(groups.replication).length > 0 ? { replication: groups.replication } : {}),
  };
}

export function summarizePcsmOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const include = readStringArray(record, 'includeNamespaces') ?? [];
  const exclude = readStringArray(record, 'excludeNamespaces') ?? [];
  const pause = readBoolean(record, 'pauseOnInitialSync') ?? false;
  const tuning = PCSM_TUNING_FIELDS.map(({ field, group, key }) => {
    const value = readNumber(readRecord(record, group) ?? {}, key);
    return {
      label: Messages.tuning[field].label,
      value: value === undefined ? Messages.summary.pcsmDefault : String(value),
    };
  });
  return [
    { label: Messages.summary.includeNamespaces, value: include.length > 0 ? include.join(', ') : Messages.summary.all },
    { label: Messages.summary.excludeNamespaces, value: exclude.length > 0 ? exclude.join(', ') : Messages.summary.none },
    { label: Messages.summary.pauseOnInitialSync, value: pause ? Messages.summary.yes : Messages.summary.no },
    ...tuning,
  ];
}

export function validateMongoUri(uri: string): string | undefined {
  const trimmed = uri.trim();
  if (trimmed === '') return Messages.uriRequired;
  const scheme = MONGODB_URI_SCHEMES.find((prefix) => trimmed.toLowerCase().startsWith(prefix));
  if (!scheme) return Messages.uriScheme;
  const rest = trimmed.slice(scheme.length);
  const authority = rest.slice(rest.lastIndexOf('@') + 1).split(/[/?]/)[0];
  if (authority.trim() === '') return Messages.uriHost;
  return undefined;
}

export function updateTuning(draft: PcsmOptionsDraft, field: PcsmTuningField, value: string): PcsmOptionsDraft {
  return { ...draft, tuning: { ...draft.tuning, [field]: value } };
}
