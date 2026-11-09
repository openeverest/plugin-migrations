import { defineEngine } from '../define-engine';
import {
  MYSQL_REPLICATION_ENGINE_ID,
  MYSQL_REPLICATION_SOURCE_PROVIDERS,
  MYSQL_REPLICATION_TARGET_PROVIDERS,
} from './mysql-replication.constants';
import { Messages } from './mysql-replication.messages';
import { MysqlReplicationOptionsForm } from './mysql-replication-options-form';
import {
  defaultMysqlReplicationOptions,
  isMysqlReplicationOptionsDraft,
  summarizeMysqlReplicationOptions,
  toMysqlReplicationRequestOptions,
  validateMysqlReplicationOptions,
  validateMysqlUri,
} from './mysql-replication.utils';

export const mysqlReplicationEngine = defineEngine({
  id: MYSQL_REPLICATION_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: MYSQL_REPLICATION_SOURCE_PROVIDERS,
  targetProviders: MYSQL_REPLICATION_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateMysqlUri },
  defaultOptions: defaultMysqlReplicationOptions,
  isOptions: isMysqlReplicationOptionsDraft,
  validateOptions: validateMysqlReplicationOptions,
  toRequestOptions: toMysqlReplicationRequestOptions,
  summarizeOptions: summarizeMysqlReplicationOptions,
  OptionsForm: MysqlReplicationOptionsForm,
});
