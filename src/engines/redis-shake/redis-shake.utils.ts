import { isRecord, readBoolean, readStringArray } from '../../utils/guards.utils';
import { validateSingleHostUri } from '../../utils/uri.utils';
import type { EngineOptionsErrors, OptionSummaryItem } from '../engine.types';
import { MAX_EXCLUDED_PREFIXES, MAX_PREFIX_LENGTH, OWN_KEY_PREFIX, REDIS_URI_SCHEMES } from './redis-shake.constants';
import { Messages } from './redis-shake.messages';
import type { RedisShakeOptions, RedisShakeOptionsDraft } from './redis-shake.types';

export function defaultRedisShakeOptions(): RedisShakeOptionsDraft {
  return { flushTarget: false, excludeKeyPrefixes: [] };
}

export function isRedisShakeOptionsDraft(value: unknown): value is RedisShakeOptionsDraft {
  return (
    isRecord(value) &&
    typeof value.flushTarget === 'boolean' &&
    Array.isArray(value.excludeKeyPrefixes) &&
    value.excludeKeyPrefixes.every((item) => typeof item === 'string')
  );
}

const UNPRINTABLE = /[\p{C}]/u;

export function isValidKeyPrefix(prefix: string): boolean {
  return (
    prefix !== '' &&
    new TextEncoder().encode(prefix).length <= MAX_PREFIX_LENGTH &&
    !UNPRINTABLE.test(prefix) &&
    !OWN_KEY_PREFIX.startsWith(prefix)
  );
}

export function validateRedisShakeOptions(draft: RedisShakeOptionsDraft): EngineOptionsErrors {
  const invalid = draft.excludeKeyPrefixes.filter((prefix) => !isValidKeyPrefix(prefix));
  if (invalid.length > 0) {
    return { excludeKeyPrefixes: Messages.invalidPrefixes(invalid, MAX_PREFIX_LENGTH, OWN_KEY_PREFIX) };
  }
  if (draft.excludeKeyPrefixes.length > MAX_EXCLUDED_PREFIXES) {
    return { excludeKeyPrefixes: Messages.tooManyPrefixes(MAX_EXCLUDED_PREFIXES) };
  }
  return {};
}

export function toRedisShakeRequestOptions(draft: RedisShakeOptionsDraft): RedisShakeOptions {
  return { flushTarget: draft.flushTarget, excludeKeyPrefixes: [...draft.excludeKeyPrefixes] };
}

export function summarizeRedisShakeOptions(options: unknown): OptionSummaryItem[] {
  const record = isRecord(options) ? options : {};
  const prefixes = readStringArray(record, 'excludeKeyPrefixes') ?? [];
  return [
    {
      label: Messages.summary.excludeKeyPrefixes,
      value: prefixes.length > 0 ? prefixes.join(', ') : Messages.summary.none,
    },
    {
      label: Messages.summary.flushTarget,
      value: readBoolean(record, 'flushTarget') ? Messages.summary.yes : Messages.summary.no,
    },
  ];
}

export function validateRedisUri(uri: string): string | undefined {
  return validateSingleHostUri(uri, REDIS_URI_SCHEMES);
}
