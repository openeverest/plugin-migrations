import { defineEngine } from '../define-engine';
import {
  REDIS_SHAKE_ENGINE_ID,
  REDIS_SHAKE_SOURCE_PROVIDERS,
  REDIS_SHAKE_TARGET_PROVIDERS,
} from './redis-shake.constants';
import { Messages } from './redis-shake.messages';
import { RedisShakeOptionsForm } from './redis-shake-options-form';
import {
  defaultRedisShakeOptions,
  isRedisShakeOptionsDraft,
  summarizeRedisShakeOptions,
  toRedisShakeRequestOptions,
  validateRedisShakeOptions,
  validateRedisUri,
} from './redis-shake.utils';

export const redisShakeEngine = defineEngine({
  id: REDIS_SHAKE_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: REDIS_SHAKE_SOURCE_PROVIDERS,
  targetProviders: REDIS_SHAKE_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateRedisUri },
  defaultOptions: defaultRedisShakeOptions,
  isOptions: isRedisShakeOptionsDraft,
  validateOptions: validateRedisShakeOptions,
  toRequestOptions: toRedisShakeRequestOptions,
  summarizeOptions: summarizeRedisShakeOptions,
  OptionsForm: RedisShakeOptionsForm,
});
