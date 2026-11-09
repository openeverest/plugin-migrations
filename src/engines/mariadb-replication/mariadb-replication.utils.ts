import { validateMysqlCompatibleUri } from '../mysql-replication/mysql-replication.utils';
import { MARIADB_URI_SCHEMES } from './mariadb-replication.constants';

export function validateMariadbUri(uri: string): string | undefined {
  return validateMysqlCompatibleUri(uri, MARIADB_URI_SCHEMES);
}
