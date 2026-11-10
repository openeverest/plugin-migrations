export interface EngineDescriptor {
  id: string;
  displayName: string;
  sourceDatabaseTypes: string[];
  targetDatabaseTypes: string[];
  targetProviders: string[];
  canPause?: boolean;
  requiresBackupStorage?: boolean;
}

export interface PluginStatus {
  status: string;
  storage: string;
  engines: EngineDescriptor[];
}
