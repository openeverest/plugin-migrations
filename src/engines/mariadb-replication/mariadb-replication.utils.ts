import { validateSingleHostUri } from '../../utils/uri.utils';
import { MARIADB_URI_SCHEMES } from './mariadb-replication.constants';

export function validateMariadbUri(uri: string): string | undefined {
  return validateSingleHostUri(uri, MARIADB_URI_SCHEMES);
}
