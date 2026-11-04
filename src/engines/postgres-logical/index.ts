import { defineEngine } from '../define-engine';
import {
  POSTGRES_LOGICAL_ENGINE_ID,
  POSTGRES_LOGICAL_SOURCE_PROVIDERS,
  POSTGRES_LOGICAL_TARGET_PROVIDERS,
} from './postgres-logical.constants';
import { Messages } from './postgres-logical.messages';
import { PostgresLogicalOptionsForm } from './postgres-logical-options-form';
import {
  defaultPostgresLogicalOptions,
  isPostgresLogicalOptionsDraft,
  summarizePostgresLogicalOptions,
  toPostgresLogicalRequestOptions,
  validatePostgresLogicalOptions,
  validatePostgresUri,
} from './postgres-logical.utils';

export const postgresLogicalEngine = defineEngine({
  id: POSTGRES_LOGICAL_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: POSTGRES_LOGICAL_SOURCE_PROVIDERS,
  targetProviders: POSTGRES_LOGICAL_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validatePostgresUri },
  defaultOptions: defaultPostgresLogicalOptions,
  isOptions: isPostgresLogicalOptionsDraft,
  validateOptions: validatePostgresLogicalOptions,
  toRequestOptions: toPostgresLogicalRequestOptions,
  summarizeOptions: summarizePostgresLogicalOptions,
  OptionsForm: PostgresLogicalOptionsForm,
});
