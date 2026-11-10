import type { ComponentType } from 'react';

export interface EngineOptionsFormProps<TOptions> {
  value: TOptions;
  onChange: (value: TOptions) => void;
  disabled?: boolean;
}

// Keyed by option field name; empty when the options are valid.
export type EngineOptionsErrors = Readonly<Record<string, string>>;

export interface OptionSummaryItem {
  label: string;
  value: string;
}

export interface ExternalSourceDefinition {
  label: string;
  uriLabel: string;
  uriPlaceholder: string;
  uriHelperText: string;
  caCertLabel: string;
  caCertHelperText: string;
  validateUri: (uri: string) => string | undefined;
}

interface EngineMetadata {
  id: string;
  label: string;
  description: string;
  sourceProviders: readonly string[];
  targetProviders: readonly string[];
  /** Omit when the engine cannot read from a database outside OpenEverest. */
  externalSource?: ExternalSourceDefinition;
  /** The engine stages data in a BackupStorage of the target's namespace. */
  requiresBackupStorage?: boolean;
  /** Describes an options snapshot (from a draft or the API) for review and details. */
  summarizeOptions: (options: unknown) => OptionSummaryItem[];
}

/** What an engine author implements, typed by the engine's own form draft. */
export interface EngineDefinition<TOptions> extends EngineMetadata {
  defaultOptions: () => TOptions;
  isOptions: (value: unknown) => value is TOptions;
  validateOptions: (options: TOptions) => EngineOptionsErrors;
  toRequestOptions: (options: TOptions) => Record<string, unknown>;
  OptionsForm: ComponentType<EngineOptionsFormProps<TOptions>>;
}

/** The type-erased engine the shared pages work with; built by defineEngine. */
export interface Engine extends EngineMetadata {
  defaultOptions: () => unknown;
  validateOptions: (options: unknown) => EngineOptionsErrors;
  toRequestOptions: (options: unknown) => Record<string, unknown>;
  OptionsForm: ComponentType<EngineOptionsFormProps<unknown>>;
}
