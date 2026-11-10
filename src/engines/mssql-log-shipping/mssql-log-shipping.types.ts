export interface MssqlLogShippingOptionsDraft {
  databases: string[];
  recreateDatabases: boolean;
  copyLogins: boolean;
  leaveSourceRestoring: boolean;
  ignoreRecentLogBackups: boolean;
  /** Text field value; empty uses the default. */
  logBackupIntervalSeconds: string;
}

export type MssqlLogShippingOptions = {
  databases: string[];
  recreateDatabases: boolean;
  copyLogins: boolean;
  leaveSourceRestoring: boolean;
  ignoreRecentLogBackups: boolean;
  logBackupIntervalSeconds?: number;
};
