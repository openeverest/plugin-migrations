import { Alert, FormControlLabel, FormHelperText, Stack, Switch, TextField } from '@mui/material';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { Messages } from './postgres-logical.messages';
import type { PostgresLogicalOptionsDraft } from './postgres-logical.types';
import { isValidSchema, isValidTable, validatePostgresLogicalOptions } from './postgres-logical.utils';

export const PostgresLogicalOptionsForm = ({
  value,
  onChange,
  disabled,
}: EngineOptionsFormProps<PostgresLogicalOptionsDraft>) => {
  const errors = validatePostgresLogicalOptions(value);

  return (
    <Stack spacing={3}>
      <Alert severity="info">{Messages.notes}</Alert>
      <TextField
        label={Messages.database.label}
        value={value.database}
        disabled={disabled}
        onChange={(event) => onChange({ ...value, database: event.target.value })}
        error={errors.database !== undefined}
        helperText={errors.database ?? Messages.database.helper}
      />
      <ChipListInput
        label={Messages.includeSchemas.label}
        helperText={Messages.includeSchemas.helper}
        value={value.includeSchemas}
        isValidItem={isValidSchema}
        error={errors.includeSchemas}
        disabled={disabled}
        onChange={(includeSchemas) => onChange({ ...value, includeSchemas })}
      />
      <ChipListInput
        label={Messages.excludeSchemas.label}
        helperText={Messages.excludeSchemas.helper}
        value={value.excludeSchemas}
        isValidItem={isValidSchema}
        error={errors.excludeSchemas}
        disabled={disabled}
        onChange={(excludeSchemas) => onChange({ ...value, excludeSchemas })}
      />
      <ChipListInput
        label={Messages.excludeTables.label}
        helperText={Messages.excludeTables.helper}
        value={value.excludeTables}
        isValidItem={isValidTable}
        error={errors.excludeTables}
        disabled={disabled}
        onChange={(excludeTables) => onChange({ ...value, excludeTables })}
      />
      <div>
        <FormControlLabel
          label={Messages.recreateDatabase.label}
          control={
            <Switch
              checked={value.recreateDatabase}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, recreateDatabase: event.target.checked })}
            />
          }
        />
        <FormHelperText>{Messages.recreateDatabase.helper}</FormHelperText>
      </div>
    </Stack>
  );
};
