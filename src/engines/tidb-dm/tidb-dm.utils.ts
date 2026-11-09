import { isRecord, readBoolean, readNumber, readStringArray } from '../../utils/guards.utils';
import { validateSingleHostUri } from '../../utils/uri.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import {
  COPY_THREADS,
  MYSQL_URI_SCHEMES,
  NAME_PATTERN,
  RESERVED_DATABASES,
  RESERVED_DATABASE_PREFIX,
  STORAGE_GIB,
  SYNC_THREADS,
} from './tidb-dm.constants';
import { Messages } from './tidb-dm.messages';
import type { TidbDmOptions, TidbDmOptionsDraft } from './tidb-dm.types';

export function defaultTidbDmOptions(): TidbDmOptionsDraft {
  return {
    databases: [],
    excludeTables: [],
    recreateDatabases: false,
    storageGiB: String(STORAGE_GIB.default),
    copyThreads: '',
    syncThreads: '',
  };
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

export function isTidbDmOptionsDraft(value: unknown): value is TidbDmOptionsDraft {
  return (
    isRecord(value) &&
    isStringArray(value.databases) &&
    isStringArray(value.excludeTables) &&
    typeof value.recreateDatabases === 'boolean' &&
    typeof value.storageGiB === 'string' &&
    typeof value.copyThreads === 'string' &&
    typeof value.syncThreads === 'string'
  );
}

export function isValidDatabase(name: string): boolean {
  const lower = name.toLowerCase();
  return NAME_PATTERN.test(name) && !RESERVED_DATABASES.includes(lower) && !lower.startsWith(RESERVED_DATABASE_PREFIX);
}

export function isValidTableName(name: string): boolean {
  const parts = name.split('.');
  return parts.length === 2 && parts.every((part) => NAME_PATTERN.test(part)) && isValidDatabase(parts[0]);
}

function parseInteger(value: string): number | undefined {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

const integerSettings = [
  ['storageGiB', STORAGE_GIB],
  ['copyThreads', COPY_THREADS],
  ['syncThreads', SYNC_THREADS],
] as const;

export function validateTidbDmOptions(draft: TidbDmOptionsDraft): EngineOptionsErrors {
  const errors: Record<string, string> = {};
  const invalidDatabases = draft.databases.filter((name) => !isValidDatabase(name));
  if (invalidDatabases.length > 0) errors.databases = Messages.invalidDatabases(invalidDatabases);
  // With no databases listed, the URI's database is used; the backend checks the tables then.
  const invalidTables = draft.excludeTables.filter(
    (name) =>
      !isValidTableName(name) || (draft.databases.length > 0 && !draft.databases.includes(name.split('.')[0])),
  );
  if (invalidTables.length > 0) errors.excludeTables = Messages.invalidTables(invalidTables);
  for (const [field, range] of integerSettings) {
    if (draft[field].trim() === '') continue;
    const value = parseInteger(draft[field]);
    if (value === undefined || value < range.min || value > range.max) {
      errors[field] = Messages.integerRange(range.min, range.max);
    }
  }
  return errors;
}

export function toTidbDmRequestOptions(draft: TidbDmOptionsDraft): TidbDmOptions {
  const options: TidbDmOptions = {
    databases: [...draft.databases],
    excludeTables: [...draft.excludeTables],
    recreateDatabases: draft.recreateDatabases,
  };
  for (const [field] of integerSettings) {
    const value = parseInteger(draft[field]);
    if (value !== undefined) options[field] = value;
  }
  return options;
}

export function summarizeTidbDmOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const list = (key: string, empty: string) => {
    const values = readStringArray(record, key) ?? [];
    return values.length > 0 ? values.join(', ') : empty;
  };
  return [
    { label: Messages.summary.databases, value: list('databases', Messages.summary.fromUri) },
    { label: Messages.summary.excludeTables, value: list('excludeTables', Messages.summary.none) },
    {
      label: Messages.summary.recreateDatabases,
      value: readBoolean(record, 'recreateDatabases') ? Messages.summary.yes : Messages.summary.no,
    },
    {
      label: Messages.summary.storageGiB,
      value: Messages.summary.gib(readNumber(record, 'storageGiB') ?? STORAGE_GIB.default),
    },
    { label: Messages.summary.copyThreads, value: String(readNumber(record, 'copyThreads') ?? COPY_THREADS.default) },
    { label: Messages.summary.syncThreads, value: String(readNumber(record, 'syncThreads') ?? SYNC_THREADS.default) },
  ];
}

export function validateTidbDmUri(uri: string): string | undefined {
  return validateSingleHostUri(uri, MYSQL_URI_SCHEMES);
}
