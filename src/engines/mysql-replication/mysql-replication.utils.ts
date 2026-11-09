import { isRecord, readBoolean, readNumber, readStringArray } from '../../utils/guards.utils';
import { validateSingleHostUri } from '../../utils/uri.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import { COPY_THREADS, MYSQL_URI_SCHEMES, NAME_PATTERN, SYSTEM_DATABASES } from './mysql-replication.constants';
import { Messages } from './mysql-replication.messages';
import type { MysqlReplicationOptions, MysqlReplicationOptionsDraft } from './mysql-replication.types';

export function defaultMysqlReplicationOptions(): MysqlReplicationOptionsDraft {
  return { databases: [], excludeTables: [], recreateDatabases: false, copyThreads: '' };
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

export function isMysqlReplicationOptionsDraft(value: unknown): value is MysqlReplicationOptionsDraft {
  return (
    isRecord(value) &&
    isStringArray(value.databases) &&
    isStringArray(value.excludeTables) &&
    typeof value.recreateDatabases === 'boolean' &&
    typeof value.copyThreads === 'string'
  );
}

export function isValidDatabase(name: string): boolean {
  return NAME_PATTERN.test(name) && !SYSTEM_DATABASES.includes(name.toLowerCase());
}

export function isValidTableName(name: string): boolean {
  const parts = name.split('.');
  return parts.length === 2 && parts.every((part) => NAME_PATTERN.test(part)) && isValidDatabase(parts[0]);
}

function parseCopyThreads(value: string): number | undefined {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

export function validateMysqlReplicationOptions(draft: MysqlReplicationOptionsDraft): EngineOptionsErrors {
  const errors: Record<string, string> = {};
  const invalidDatabases = draft.databases.filter((name) => !isValidDatabase(name));
  if (invalidDatabases.length > 0) errors.databases = Messages.invalidDatabases(invalidDatabases);
  // With no databases listed, the URI's database is used; the backend checks the tables then.
  const invalidTables = draft.excludeTables.filter(
    (name) =>
      !isValidTableName(name) || (draft.databases.length > 0 && !draft.databases.includes(name.split('.')[0])),
  );
  if (invalidTables.length > 0) errors.excludeTables = Messages.invalidTables(invalidTables);
  if (draft.copyThreads.trim() !== '') {
    const threads = parseCopyThreads(draft.copyThreads);
    if (threads === undefined || threads < COPY_THREADS.min || threads > COPY_THREADS.max) {
      errors.copyThreads = Messages.integerRange(COPY_THREADS.min, COPY_THREADS.max);
    }
  }
  return errors;
}

export function toMysqlReplicationRequestOptions(draft: MysqlReplicationOptionsDraft): MysqlReplicationOptions {
  const copyThreads = parseCopyThreads(draft.copyThreads);
  return {
    databases: [...draft.databases],
    excludeTables: [...draft.excludeTables],
    recreateDatabases: draft.recreateDatabases,
    ...(copyThreads !== undefined ? { copyThreads } : {}),
  };
}

export function summarizeMysqlReplicationOptions(options: unknown): OptionSummaryItem[] {
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
    { label: Messages.summary.copyThreads, value: String(readNumber(record, 'copyThreads') ?? COPY_THREADS.default) },
  ];
}

export function validateMysqlUri(uri: string): string | undefined {
  return validateSingleHostUri(uri, MYSQL_URI_SCHEMES);
}
