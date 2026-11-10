import { defineEngine } from '../define-engine';
import { TIDB_DM_ENGINE_ID, TIDB_DM_SOURCE_PROVIDERS, TIDB_DM_TARGET_PROVIDERS } from './tidb-dm.constants';
import { Messages } from './tidb-dm.messages';
import { TidbDmOptionsForm } from './tidb-dm-options-form';
import {
  defaultTidbDmOptions,
  isTidbDmOptionsDraft,
  summarizeTidbDmOptions,
  toTidbDmRequestOptions,
  validateTidbDmOptions,
  validateTidbDmUri,
} from './tidb-dm.utils';

export const tidbDmEngine = defineEngine({
  id: TIDB_DM_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: TIDB_DM_SOURCE_PROVIDERS,
  targetProviders: TIDB_DM_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateTidbDmUri },
  defaultOptions: defaultTidbDmOptions,
  isOptions: isTidbDmOptionsDraft,
  validateOptions: validateTidbDmOptions,
  toRequestOptions: toTidbDmRequestOptions,
  summarizeOptions: summarizeTidbDmOptions,
  OptionsForm: TidbDmOptionsForm,
});
