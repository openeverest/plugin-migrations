import type { Engine, EngineDefinition, EngineOptionsFormProps } from './engine.types';

// Erases an engine's option type so engines with different option shapes can
// share one registry. Options that do not match the engine (e.g. left over from
// another engine) fall back to the engine defaults.
export function defineEngine<TOptions>(definition: EngineDefinition<TOptions>): Engine {
  const { defaultOptions, isOptions, validateOptions, toRequestOptions, OptionsForm: TypedOptionsForm, ...metadata } =
    definition;
  const readOptions = (value: unknown): TOptions => (isOptions(value) ? value : defaultOptions());

  const OptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<unknown>) => (
    <TypedOptionsForm value={readOptions(value)} onChange={onChange} disabled={disabled} />
  );
  OptionsForm.displayName = `${definition.id}OptionsForm`;

  return {
    ...metadata,
    defaultOptions,
    validateOptions: (value) => validateOptions(readOptions(value)),
    toRequestOptions: (value) => toRequestOptions(readOptions(value)),
    OptionsForm,
  };
}
