export type PcsmTuningField =
  | 'cloneNumParallelCollections'
  | 'cloneNumReadWorkers'
  | 'cloneNumInsertWorkers'
  | 'replicationNumWorkers';

export interface PcsmTuningFieldSpec {
  field: PcsmTuningField;
  /** Location in the request/snapshot options: options[group][key]. */
  group: 'clone' | 'replication';
  key: string;
  min: number;
  max: number;
}

/** Form state: integers are kept as text so an empty field means "PCSM default". */
export interface PcsmOptionsDraft {
  includeNamespaces: string[];
  excludeNamespaces: string[];
  pauseOnInitialSync: boolean;
  tuning: Record<PcsmTuningField, string>;
}

export type PcsmOptions = {
  includeNamespaces: string[];
  excludeNamespaces: string[];
  pauseOnInitialSync: boolean;
  clone?: Record<string, number>;
  replication?: Record<string, number>;
};
