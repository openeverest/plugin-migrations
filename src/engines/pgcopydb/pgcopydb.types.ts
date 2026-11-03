export type PgcopydbTuningField = 'tableJobs' | 'indexJobs';

export interface PgcopydbTuningFieldSpec {
  field: PgcopydbTuningField;
  min: number;
  max: number;
}

/** Form state: integers are kept as text so an empty field means "pgcopydb default". */
export interface PgcopydbOptionsDraft {
  database: string;
  includeSchemas: string[];
  excludeSchemas: string[];
  excludeTables: string[];
  dropIfExists: boolean;
  tuning: Record<PgcopydbTuningField, string>;
}

export type PgcopydbOptions = {
  database?: string;
  includeSchemas: string[];
  excludeSchemas: string[];
  excludeTables: string[];
  dropIfExists: boolean;
  tableJobs?: number;
  indexJobs?: number;
};
