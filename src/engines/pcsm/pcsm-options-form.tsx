import { FormControlLabel, FormHelperText, Stack, Switch } from '@mui/material';
import type { EngineOptionsFormProps } from '../engine.types';
import { NamespaceListInput } from './namespace-list-input/namespace-list-input';
import { Messages } from './pcsm.messages';
import type { PcsmOptionsDraft } from './pcsm.types';
import { updateTuning, validatePcsmOptions } from './pcsm.utils';
import { TuningOptions } from './tuning-options/tuning-options';

export const PcsmOptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<PcsmOptionsDraft>) => {
  const errors = validatePcsmOptions(value);

  return (
    <Stack spacing={3}>
      <NamespaceListInput
        label={Messages.includeNamespaces.label}
        helperText={Messages.includeNamespaces.helper}
        value={value.includeNamespaces}
        error={errors.includeNamespaces}
        disabled={disabled}
        onChange={(includeNamespaces) => onChange({ ...value, includeNamespaces })}
      />
      <NamespaceListInput
        label={Messages.excludeNamespaces.label}
        helperText={Messages.excludeNamespaces.helper}
        value={value.excludeNamespaces}
        error={errors.excludeNamespaces}
        disabled={disabled}
        onChange={(excludeNamespaces) => onChange({ ...value, excludeNamespaces })}
      />
      <div>
        <FormControlLabel
          label={Messages.pauseOnInitialSync.label}
          control={
            <Switch
              checked={value.pauseOnInitialSync}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, pauseOnInitialSync: event.target.checked })}
            />
          }
        />
        <FormHelperText>{Messages.pauseOnInitialSync.helper}</FormHelperText>
      </div>
      <TuningOptions
        value={value.tuning}
        errors={errors}
        disabled={disabled}
        onChange={(field, next) => onChange(updateTuning(value, field, next))}
      />
    </Stack>
  );
};
