import { defineEngine } from '../define-engine';
import { PCSM_ENGINE_ID, PCSM_PROVIDERS } from './pcsm.constants';
import { Messages } from './pcsm.messages';
import { PcsmOptionsForm } from './pcsm-options-form';
import {
  defaultPcsmOptions,
  isPcsmOptionsDraft,
  summarizePcsmOptions,
  toPcsmRequestOptions,
  validateMongoUri,
  validatePcsmOptions,
} from './pcsm.utils';

export const pcsmEngine = defineEngine({
  id: PCSM_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: PCSM_PROVIDERS,
  targetProviders: PCSM_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateMongoUri },
  defaultOptions: defaultPcsmOptions,
  isOptions: isPcsmOptionsDraft,
  validateOptions: validatePcsmOptions,
  toRequestOptions: toPcsmRequestOptions,
  summarizeOptions: summarizePcsmOptions,
  OptionsForm: PcsmOptionsForm,
});
