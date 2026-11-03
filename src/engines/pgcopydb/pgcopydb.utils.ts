import { isRecord, readBoolean, readNumber, readString, readStringArray } from '../../utils/guards.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import {
  DATABASE_PATTERN,
  IDENTIFIER_PATTERN,
  PGCOPYDB_TUNING_FIELDS,
  POSTGRES_URI_SCHEMES,
  RESERVED_SCHEMA,
} from './pgcopydb.constants';
import { Messages } from './pgcopydb.messages';
import type {
  PgcopydbOptions,
  PgcopydbOptionsDraft,
  PgcopydbTuningField,
  PgcopydbTuningFieldSpec,
} from './pgcopydb.types';

export function defaultPgcopydbOptions(): PgcopydbOptionsDraft {
  return {
    database: '',
    includeSchemas: [],
    excludeSchemas: [],
    excludeTables: [],
    dropIfExists: false,
    tuning: { tableJobs: '', indexJobs: '' },
  };
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

export function isPgcopydbOptionsDraft(value: unknown): value is PgcopydbOptionsDraft {
  if (!isRecord(value) || !isRecord(value.tuning)) return false;
  const { tuning } = value;
  return (
    typeof value.database === 'string' &&
    isStringArray(value.includeSchemas) &&
    isStringArray(value.excludeSchemas) &&
    isStringArray(value.excludeTables) &&
    typeof value.dropIfExists === 'boolean' &&
    PGCOPYDB_TUNING_FIELDS.every(({ field }) => typeof tuning[field] === 'string')
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

function parseTuningValue(value: string): number | undefined {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

function validateTuningValue(value: string, { min, max }: PgcopydbTuningFieldSpec): string | undefined {
  if (value.trim() === '') return undefined;
  const parsed = parseTuningValue(value);
  if (parsed === undefined || parsed < min || parsed > max) return Messages.integerRange(min, max);
  return undefined;
}

export function validatePgcopydbOptions(draft: PgcopydbOptionsDraft): EngineOptionsErrors {
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
  for (const spec of PGCOPYDB_TUNING_FIELDS) {
    const error = validateTuningValue(draft.tuning[spec.field], spec);
    if (error) errors[spec.field] = error;
  }
  return errors;
}

export function toPgcopydbRequestOptions(draft: PgcopydbOptionsDraft): PgcopydbOptions {
  const database = draft.database.trim();
  const tuning: Partial<Record<PgcopydbTuningField, number>> = {};
  for (const { field } of PGCOPYDB_TUNING_FIELDS) {
    const value = parseTuningValue(draft.tuning[field]);
    if (value !== undefined) tuning[field] = value;
  }
  return {
    ...(database !== '' ? { database } : {}),
    includeSchemas: [...draft.includeSchemas],
    excludeSchemas: [...draft.excludeSchemas],
    excludeTables: [...draft.excludeTables],
    dropIfExists: draft.dropIfExists,
    ...tuning,
  };
}

export function summarizePgcopydbOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const list = (key: string, empty: string) => {
    const values = readStringArray(record, key) ?? [];
    return values.length > 0 ? values.join(', ') : empty;
  };
  const tuning = PGCOPYDB_TUNING_FIELDS.map(({ field }) => {
    const value = readNumber(record, field);
    return {
      label: Messages.tuning[field].label,
      value: value === undefined ? Messages.summary.pgcopydbDefault : String(value),
    };
  });
  return [
    { label: Messages.summary.database, value: readString(record, 'database') || Messages.summary.fromUri },
    { label: Messages.summary.includeSchemas, value: list('includeSchemas', Messages.summary.all) },
    { label: Messages.summary.excludeSchemas, value: list('excludeSchemas', Messages.summary.none) },
    { label: Messages.summary.excludeTables, value: list('excludeTables', Messages.summary.none) },
    {
      label: Messages.summary.dropIfExists,
      value: readBoolean(record, 'dropIfExists') ? Messages.summary.yes : Messages.summary.no,
    },
    ...tuning,
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

export function updateTuning(
  draft: PgcopydbOptionsDraft,
  field: PgcopydbTuningField,
  value: string,
): PgcopydbOptionsDraft {
  return { ...draft, tuning: { ...draft.tuning, [field]: value } };
}
