import { isRecord, readBoolean, readString, readStringArray } from '../../utils/guards.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import { DATABASE_PATTERN, IDENTIFIER_PATTERN, POSTGRES_URI_SCHEMES, RESERVED_SCHEMA } from './postgres-logical.constants';
import { Messages } from './postgres-logical.messages';
import type { PostgresLogicalOptions, PostgresLogicalOptionsDraft } from './postgres-logical.types';

export function defaultPostgresLogicalOptions(): PostgresLogicalOptionsDraft {
  return { database: '', includeSchemas: [], excludeSchemas: [], excludeTables: [], recreateDatabase: false };
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

export function isPostgresLogicalOptionsDraft(value: unknown): value is PostgresLogicalOptionsDraft {
  return (
    isRecord(value) &&
    typeof value.database === 'string' &&
    isStringArray(value.includeSchemas) &&
    isStringArray(value.excludeSchemas) &&
    isStringArray(value.excludeTables) &&
    typeof value.recreateDatabase === 'boolean'
  );
}

export function isValidSchema(name: string): boolean {
  return IDENTIFIER_PATTERN.test(name) && name !== RESERVED_SCHEMA;
}

export function isValidTable(name: string): boolean {
  const parts = name.split('.');
  return parts.length === 2 && parts.every((part) => IDENTIFIER_PATTERN.test(part)) && parts[0] !== RESERVED_SCHEMA;
}

function validateSchemas(names: readonly string[]): string | undefined {
  if (names.includes(RESERVED_SCHEMA)) return Messages.reservedSchema(RESERVED_SCHEMA);
  const invalid = names.filter((name) => !isValidSchema(name));
  return invalid.length > 0 ? Messages.invalidSchemas(invalid) : undefined;
}

export function validatePostgresLogicalOptions(draft: PostgresLogicalOptionsDraft): EngineOptionsErrors {
  const errors: Record<string, string> = {};
  const database = draft.database.trim();
  if (database !== '' && !DATABASE_PATTERN.test(database)) errors.database = Messages.database.invalid;
  const include = validateSchemas(draft.includeSchemas);
  if (include) errors.includeSchemas = include;
  const exclude = validateSchemas(draft.excludeSchemas);
  if (exclude) errors.excludeSchemas = exclude;
  else if (draft.includeSchemas.length > 0 && draft.excludeSchemas.length > 0) {
    errors.excludeSchemas = Messages.includeAndExclude;
  }
  const invalidTables = draft.excludeTables.filter((name) => !isValidTable(name));
  if (invalidTables.length > 0) errors.excludeTables = Messages.invalidTables(invalidTables);
  return errors;
}

export function toPostgresLogicalRequestOptions(draft: PostgresLogicalOptionsDraft): PostgresLogicalOptions {
  const database = draft.database.trim();
  return {
    ...(database !== '' ? { database } : {}),
    includeSchemas: [...draft.includeSchemas],
    excludeSchemas: [...draft.excludeSchemas],
    excludeTables: [...draft.excludeTables],
    recreateDatabase: draft.recreateDatabase,
  };
}

export function summarizePostgresLogicalOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const list = (key: string, empty: string) => {
    const values = readStringArray(record, key) ?? [];
    return values.length > 0 ? values.join(', ') : empty;
  };
  return [
    { label: Messages.summary.database, value: readString(record, 'database') || Messages.summary.fromUri },
    { label: Messages.summary.includeSchemas, value: list('includeSchemas', Messages.summary.all) },
    { label: Messages.summary.excludeSchemas, value: list('excludeSchemas', Messages.summary.none) },
    { label: Messages.summary.excludeTables, value: list('excludeTables', Messages.summary.none) },
    {
      label: Messages.summary.recreateDatabase,
      value: readBoolean(record, 'recreateDatabase') ? Messages.summary.yes : Messages.summary.no,
    },
  ];
}

export function validatePostgresUri(uri: string): string | undefined {
  const trimmed = uri.trim();
  if (trimmed === '') return Messages.uriRequired;
  const scheme = POSTGRES_URI_SCHEMES.find((prefix) => trimmed.toLowerCase().startsWith(prefix));
  if (!scheme) return Messages.uriScheme;
  const rest = trimmed.slice(scheme.length);
  const authorityEnd = rest.search(/[/?#]/);
  const authority = authorityEnd < 0 ? rest : rest.slice(0, authorityEnd);
  const hosts = authority.slice(authority.lastIndexOf('@') + 1);
  if (hosts.trim() === '') return Messages.uriHost;
  if (hosts.includes(',')) return Messages.uriMultiHost;
  return undefined;
}
