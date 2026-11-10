import { defineEngine } from '../define-engine';
import { MSSQL_LOG_SHIPPING_ENGINE_ID, MSSQL_PROVIDERS } from './mssql-log-shipping.constants';
import { Messages } from './mssql-log-shipping.messages';
import { MssqlLogShippingOptionsForm } from './mssql-log-shipping-options-form';
import {
  defaultMssqlLogShippingOptions,
  isMssqlLogShippingOptionsDraft,
  summarizeMssqlLogShippingOptions,
  toMssqlLogShippingRequestOptions,
  validateMssqlLogShippingOptions,
  validateSqlServerUri,
} from './mssql-log-shipping.utils';

export const mssqlLogShippingEngine = defineEngine({
  id: MSSQL_LOG_SHIPPING_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: MSSQL_PROVIDERS,
  targetProviders: MSSQL_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateSqlServerUri },
  requiresBackupStorage: true,
  defaultOptions: defaultMssqlLogShippingOptions,
  isOptions: isMssqlLogShippingOptionsDraft,
  validateOptions: validateMssqlLogShippingOptions,
  toRequestOptions: toMssqlLogShippingRequestOptions,
  summarizeOptions: summarizeMssqlLogShippingOptions,
  OptionsForm: MssqlLogShippingOptionsForm,
});
