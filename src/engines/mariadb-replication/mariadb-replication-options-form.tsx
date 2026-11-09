import type { EngineOptionsFormProps } from '../engine.types';
import { MysqlReplicationOptionsForm } from '../mysql-replication/mysql-replication-options-form';
import type { MysqlReplicationOptionsDraft } from '../mysql-replication/mysql-replication.types';
import { Messages } from './mariadb-replication.messages';

/** MariaDB takes the same options as MySQL; only the notes and the warning differ. */
export const MariadbReplicationOptionsForm = (props: EngineOptionsFormProps<MysqlReplicationOptionsDraft>) => (
  <MysqlReplicationOptionsForm {...props} notes={Messages.notes} warning={Messages.galeraAccountsWarning} />
);
