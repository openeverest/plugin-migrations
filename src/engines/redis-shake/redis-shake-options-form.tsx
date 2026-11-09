import { Alert, FormControlLabel, FormHelperText, Stack, Switch } from '@mui/material';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { Messages } from './redis-shake.messages';
import type { RedisShakeOptionsDraft } from './redis-shake.types';
import { isValidKeyPrefix, validateRedisShakeOptions } from './redis-shake.utils';

export const RedisShakeOptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<RedisShakeOptionsDraft>) => {
  const errors = validateRedisShakeOptions(value);

  return (
    <Stack spacing={3}>
      <Alert severity="info">{Messages.notes}</Alert>
      <ChipListInput
        label={Messages.excludeKeyPrefixes.label}
        helperText={Messages.excludeKeyPrefixes.helper}
        value={value.excludeKeyPrefixes}
        isValidItem={isValidKeyPrefix}
        error={errors.excludeKeyPrefixes}
        disabled={disabled}
        onChange={(excludeKeyPrefixes) => onChange({ ...value, excludeKeyPrefixes })}
      />
      <div>
        <FormControlLabel
          label={Messages.flushTarget.label}
          control={
            <Switch
              checked={value.flushTarget}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, flushTarget: event.target.checked })}
            />
          }
        />
        <FormHelperText>{Messages.flushTarget.helper}</FormHelperText>
      </div>
    </Stack>
  );
};
