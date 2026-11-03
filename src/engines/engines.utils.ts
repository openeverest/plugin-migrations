import type { EngineDescriptor } from '../api/status.types';
import type { Engine, OptionSummaryItem } from './engine.types';
import { Messages } from './engines.messages';

/** Engines must be implemented by this UI and enabled in the backend. */
export function availableEngines(registry: readonly Engine[], enabled: readonly EngineDescriptor[]): Engine[] {
  const enabledIds = new Set(enabled.map((engine) => engine.id));
  return registry.filter((engine) => enabledIds.has(engine.id));
}

function describeValue(value: unknown): string {
  if (value === undefined || value === null || value === '') return Messages.emptyValue;
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

/** Fallback summary for engines this UI does not know. */
export function summarizeUnknownOptions(options: unknown): OptionSummaryItem[] {
  if (typeof options !== 'object' || options === null) return [];
  return Object.entries(options).map(([label, value]) => ({ label, value: describeValue(value) }));
}
