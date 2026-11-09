export const Messages = {
  label: 'Redis / Valkey → Valkey',
  description:
    'RedisShake copies a snapshot through the replication protocol, then applies ongoing changes until you cut over. Works from Redis or Valkey, standalone or cluster.',
  externalSource: {
    label: 'External Redis or Valkey',
    uriLabel: 'Connection URI',
    uriPlaceholder: 'rediss://default:secret@redis1:6379',
    uriHelperText:
      'The primary of a standalone server or any primary of a cluster. The user needs PSYNC (replication) and write access, which the migration uses for a heartbeat key. Only database 0 can be migrated. Credentials are stored in a Kubernetes Secret.',
    caCertLabel: 'CA certificate (optional)',
    caCertHelperText:
      'PEM-encoded CA the worker uses to verify the source during its checks. RedisShake itself encrypts but does not verify the connection.',
  },
  notes:
    'Every key, TTL, stream consumer group and function library is copied; ongoing changes are applied until you cut over. User accounts (ACLs) and configuration are not copied. A migration cannot be paused, and if it is interrupted (a restart or a lost connection) the copy starts again from the beginning on an emptied target.',
  excludeKeyPrefixes: {
    label: 'Exclude key prefixes',
    helper:
      'Keys starting with these prefixes are neither copied nor replicated, e.g. cache:. A command that changes both excluded and migrated keys stops the migration. Press Enter to add.',
  },
  invalidPrefixes: (prefixes: string[], maxLength: number, reserved: string) =>
    `Invalid prefix: ${prefixes.join(', ')}. Use up to ${maxLength} printable characters; ${reserved} is reserved.`,
  tooManyPrefixes: (max: number) => `Exclude at most ${max} prefixes.`,
  flushTarget: {
    label: 'Delete all keys on the target',
    helper:
      'Deletes every key on the target instance before the copy. Leave off to fail when the target is not empty.',
  },
  summary: {
    excludeKeyPrefixes: 'Exclude key prefixes',
    flushTarget: 'Delete all keys on the target',
    none: 'None',
    yes: 'Yes',
    no: 'No',
  },
};
