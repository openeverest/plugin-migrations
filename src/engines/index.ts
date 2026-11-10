import type { Engine, OptionSummaryItem } from './engine.types';
import { summarizeUnknownOptions } from './engines.utils';
import { mariadbReplicationEngine } from './mariadb-replication';
import { mssqlLogShippingEngine } from './mssql-log-shipping';
import { mysqlReplicationEngine } from './mysql-replication';
import { pcsmEngine } from './pcsm';
import { postgresLogicalEngine } from './postgres-logical';
import { redisShakeEngine } from './redis-shake';
import { tidbDmEngine } from './tidb-dm';

export const ENGINES: readonly Engine[] = [
  pcsmEngine,
  postgresLogicalEngine,
  mysqlReplicationEngine,
  mariadbReplicationEngine,
  redisShakeEngine,
  mssqlLogShippingEngine,
  tidbDmEngine,
];

export function getEngine(id: string): Engine | undefined {
  return ENGINES.find((engine) => engine.id === id);
}

export function engineLabel(id: string): string {
  return getEngine(id)?.label ?? id;
}

export function summarizeEngineOptions(engineId: string, options: unknown): OptionSummaryItem[] {
  return getEngine(engineId)?.summarizeOptions(options) ?? summarizeUnknownOptions(options);
}
