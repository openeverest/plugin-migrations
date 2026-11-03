import { defineEngine } from '../define-engine';
import { PGCOPYDB_ENGINE_ID, PGCOPYDB_SOURCE_PROVIDERS, PGCOPYDB_TARGET_PROVIDERS } from './pgcopydb.constants';
import { Messages } from './pgcopydb.messages';
import { PgcopydbOptionsForm } from './pgcopydb-options-form';
import {
  defaultPgcopydbOptions,
  isPgcopydbOptionsDraft,
  summarizePgcopydbOptions,
  toPgcopydbRequestOptions,
  validatePgcopydbOptions,
  validatePostgresUri,
} from './pgcopydb.utils';

export const pgcopydbEngine = defineEngine({
  id: PGCOPYDB_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: PGCOPYDB_SOURCE_PROVIDERS,
  targetProviders: PGCOPYDB_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validatePostgresUri },
  defaultOptions: defaultPgcopydbOptions,
  isOptions: isPgcopydbOptionsDraft,
  validateOptions: validatePgcopydbOptions,
  toRequestOptions: toPgcopydbRequestOptions,
  summarizeOptions: summarizePgcopydbOptions,
  OptionsForm: PgcopydbOptionsForm,
});
