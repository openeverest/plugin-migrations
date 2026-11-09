export interface TidbDmOptionsDraft {
  databases: string[];
  excludeTables: string[];
  recreateDatabases: boolean;
  /** Text field values; empty uses the default. */
  storageGiB: string;
  copyThreads: string;
  syncThreads: string;
}

export type TidbDmOptions = {
  databases: string[];
  excludeTables: string[];
  recreateDatabases: boolean;
  storageGiB?: number;
  copyThreads?: number;
  syncThreads?: number;
};
