export interface MysqlReplicationOptionsDraft {
  databases: string[];
  excludeTables: string[];
  recreateDatabases: boolean;
  /** Text field value; empty uses the default. */
  copyThreads: string;
}

export type MysqlReplicationOptions = {
  databases: string[];
  excludeTables: string[];
  recreateDatabases: boolean;
  copyThreads?: number;
};
