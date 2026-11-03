export interface EngineDescriptor {
  id: string;
  displayName: string;
  sourceDatabaseTypes: string[];
  targetDatabaseTypes: string[];
  targetProviders: string[];
}

export interface PluginStatus {
  status: string;
  storage: string;
  engines: EngineDescriptor[];
}
