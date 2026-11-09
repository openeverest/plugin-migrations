export const REDIS_SHAKE_ENGINE_ID = 'redis-shake';

/** provider-valkey publishes the default user, which can serve PSYNC and empty the target. */
export const REDIS_SHAKE_TARGET_PROVIDERS = ['valkey'];

export const REDIS_SHAKE_SOURCE_PROVIDERS = ['valkey'];

export const REDIS_URI_SCHEMES = ['redis://', 'rediss://', 'valkey://', 'valkeys://'];

/** Starts the keys the migration worker writes; excluding it would break the migration. */
export const OWN_KEY_PREFIX = '__openeverest_migration:';

export const MAX_EXCLUDED_PREFIXES = 64;

export const MAX_PREFIX_LENGTH = 256;
