import type { PcsmTuningFieldSpec } from './pcsm.types';

export const PCSM_ENGINE_ID = 'pcsm';

export const PCSM_PROVIDERS = ['percona-server-mongodb'];

export const MONGODB_URI_SCHEMES = ['mongodb://', 'mongodb+srv://'];

// `db`, `db.*` or `db.coll`. Database names cannot contain /\. "$*<>:|? or
// whitespace; PCSM supports only a whole-database wildcard.
export const NAMESPACE_PATTERN = /^[^\s./\\"$*<>:|?]+(\.(\*|[^\s$*]+))?$/;

export const PCSM_TUNING_FIELDS: readonly PcsmTuningFieldSpec[] = [
  { field: 'cloneNumParallelCollections', group: 'clone', key: 'numParallelCollections', min: 1, max: 64 },
  { field: 'cloneNumReadWorkers', group: 'clone', key: 'numReadWorkers', min: 1, max: 256 },
  { field: 'cloneNumInsertWorkers', group: 'clone', key: 'numInsertWorkers', min: 1, max: 256 },
  { field: 'replicationNumWorkers', group: 'replication', key: 'numWorkers', min: 1, max: 256 },
];
