import { FormControlLabel, FormHelperText, Stack, Switch } from '@mui/material';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { Messages } from './pcsm.messages';
import type { PcsmOptionsDraft } from './pcsm.types';
import { isValidNamespace, updateTuning, validatePcsmOptions } from './pcsm.utils';
import { TuningOptions } from './tuning-options/tuning-options';

export const PcsmOptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<PcsmOptionsDraft>) => {
  const errors = validatePcsmOptions(value);

  return (
    <Stack spacing={3}>
      <ChipListInput
        label={Messages.includeNamespaces.label}
        helperText={Messages.includeNamespaces.helper}
        value={value.includeNamespaces}
        isValidItem={isValidNamespace}
        error={errors.includeNamespaces}
        disabled={disabled}
        onChange={(includeNamespaces) => onChange({ ...value, includeNamespaces })}
      />
      <ChipListInput
        label={Messages.excludeNamespaces.label}
        helperText={Messages.excludeNamespaces.helper}
        value={value.excludeNamespaces}
        isValidItem={isValidNamespace}
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
