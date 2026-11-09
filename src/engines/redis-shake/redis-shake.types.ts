export interface RedisShakeOptionsDraft {
  flushTarget: boolean;
  excludeKeyPrefixes: string[];
}

export type RedisShakeOptions = {
  flushTarget: boolean;
  excludeKeyPrefixes: string[];
};
