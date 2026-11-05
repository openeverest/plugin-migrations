export interface PostgresLogicalOptionsDraft {
  database: string;
  includeSchemas: string[];
  excludeSchemas: string[];
  excludeTables: string[];
  recreateDatabase: boolean;
}

export type PostgresLogicalOptions = {
  database?: string;
  includeSchemas: string[];
  excludeSchemas: string[];
  excludeTables: string[];
  recreateDatabase: boolean;
};
