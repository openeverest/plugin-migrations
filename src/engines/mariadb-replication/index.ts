import { defineEngine } from '../define-engine';
import {
  defaultMysqlReplicationOptions,
  isMysqlReplicationOptionsDraft,
  summarizeMysqlReplicationOptions,
  toMysqlReplicationRequestOptions,
  validateMysqlReplicationOptions,
} from '../mysql-replication/mysql-replication.utils';
import {
  MARIADB_REPLICATION_ENGINE_ID,
  MARIADB_REPLICATION_SOURCE_PROVIDERS,
  MARIADB_REPLICATION_TARGET_PROVIDERS,
} from './mariadb-replication.constants';
import { Messages } from './mariadb-replication.messages';
import { MariadbReplicationOptionsForm } from './mariadb-replication-options-form';
import { validateMariadbUri } from './mariadb-replication.utils';

export const mariadbReplicationEngine = defineEngine({
  id: MARIADB_REPLICATION_ENGINE_ID,
  label: Messages.label,
  description: Messages.description,
  sourceProviders: MARIADB_REPLICATION_SOURCE_PROVIDERS,
  targetProviders: MARIADB_REPLICATION_TARGET_PROVIDERS,
  externalSource: { ...Messages.externalSource, validateUri: validateMariadbUri },
  defaultOptions: defaultMysqlReplicationOptions,
  isOptions: isMysqlReplicationOptionsDraft,
  validateOptions: validateMysqlReplicationOptions,
  toRequestOptions: toMysqlReplicationRequestOptions,
  summarizeOptions: summarizeMysqlReplicationOptions,
  OptionsForm: MariadbReplicationOptionsForm,
});
