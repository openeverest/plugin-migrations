import { isRecord, readBoolean, readNumber, readStringArray } from '../../utils/guards.utils';
import { validateSingleHostUri } from '../../utils/uri.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import {
  LOG_BACKUP_INTERVAL,
  MAX_DATABASES,
  MSSQL_URI_SCHEMES,
  NAME_PATTERN,
  SYSTEM_DATABASES,
} from './mssql-log-shipping.constants';
import { Messages } from './mssql-log-shipping.messages';
import type { MssqlLogShippingOptions, MssqlLogShippingOptionsDraft } from './mssql-log-shipping.types';

export function defaultMssqlLogShippingOptions(): MssqlLogShippingOptionsDraft {
  return {
    databases: [],
    recreateDatabases: false,
    copyLogins: true,
    leaveSourceRestoring: false,
    ignoreRecentLogBackups: false,
    logBackupIntervalSeconds: '',
  };
}

export function isMssqlLogShippingOptionsDraft(value: unknown): value is MssqlLogShippingOptionsDraft {
  return (
    isRecord(value) &&
    Array.isArray(value.databases) &&
    value.databases.every((item) => typeof item === 'string') &&
    typeof value.recreateDatabases === 'boolean' &&
    typeof value.copyLogins === 'boolean' &&
    typeof value.leaveSourceRestoring === 'boolean' &&
    typeof value.ignoreRecentLogBackups === 'boolean' &&
    typeof value.logBackupIntervalSeconds === 'string'
  );
}

export function isValidDatabase(name: string): boolean {
  return NAME_PATTERN.test(name) && !SYSTEM_DATABASES.includes(name.toLowerCase());
}

function parseInterval(value: string): number | undefined {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

export function validateMssqlLogShippingOptions(draft: MssqlLogShippingOptionsDraft): EngineOptionsErrors {
  const errors: Record<string, string> = {};
  const invalid = draft.databases.filter((name) => !isValidDatabase(name));
  if (invalid.length > 0) errors.databases = Messages.invalidDatabases(invalid);
  else if (draft.databases.length > MAX_DATABASES) errors.databases = Messages.tooManyDatabases(MAX_DATABASES);
  if (draft.logBackupIntervalSeconds.trim() !== '') {
    const interval = parseInterval(draft.logBackupIntervalSeconds);
    if (interval === undefined || interval < LOG_BACKUP_INTERVAL.min || interval > LOG_BACKUP_INTERVAL.max) {
      errors.logBackupIntervalSeconds = Messages.integerRange(LOG_BACKUP_INTERVAL.min, LOG_BACKUP_INTERVAL.max);
    }
  }
  return errors;
}

export function toMssqlLogShippingRequestOptions(draft: MssqlLogShippingOptionsDraft): MssqlLogShippingOptions {
  const interval = parseInterval(draft.logBackupIntervalSeconds);
  return {
    databases: [...draft.databases],
    recreateDatabases: draft.recreateDatabases,
    copyLogins: draft.copyLogins,
    leaveSourceRestoring: draft.leaveSourceRestoring,
    ignoreRecentLogBackups: draft.ignoreRecentLogBackups,
    ...(interval !== undefined ? { logBackupIntervalSeconds: interval } : {}),
  };
}

export function summarizeMssqlLogShippingOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const databases = readStringArray(record, 'databases') ?? [];
  const yesNo = (key: string, fallback = false) =>
    (readBoolean(record, key) ?? fallback) ? Messages.summary.yes : Messages.summary.no;
  return [
    {
      label: Messages.summary.databases,
      value: databases.length > 0 ? databases.join(', ') : Messages.summary.fromUri,
    },
    { label: Messages.summary.recreateDatabases, value: yesNo('recreateDatabases') },
    { label: Messages.summary.copyLogins, value: yesNo('copyLogins', true) },
    { label: Messages.summary.leaveSourceRestoring, value: yesNo('leaveSourceRestoring') },
    { label: Messages.summary.ignoreRecentLogBackups, value: yesNo('ignoreRecentLogBackups') },
    {
      label: Messages.summary.logBackupInterval,
      value: Messages.summary.seconds(readNumber(record, 'logBackupIntervalSeconds') ?? LOG_BACKUP_INTERVAL.default),
    },
  ];
}

export function validateSqlServerUri(uri: string): string | undefined {
  return validateSingleHostUri(uri, MSSQL_URI_SCHEMES);
}
