import { Box, FormControlLabel, FormHelperText, Stack, Switch, TextField } from '@mui/material';
import { AdvancedSection } from '../../components/advanced-section/advanced-section';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { PGCOPYDB_TUNING_FIELDS } from './pgcopydb.constants';
import { Messages } from './pgcopydb.messages';
import type { PgcopydbOptionsDraft } from './pgcopydb.types';
import { isValidSchema, isValidTable, updateTuning, validatePgcopydbOptions } from './pgcopydb.utils';

export const PgcopydbOptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<PgcopydbOptionsDraft>) => {
  const errors = validatePgcopydbOptions(value);
  const tuningHasErrors = PGCOPYDB_TUNING_FIELDS.some(({ field }) => errors[field] !== undefined);

  return (
    <Stack spacing={3}>
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
          label={Messages.dropIfExists.label}
          control={
            <Switch
              checked={value.dropIfExists}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, dropIfExists: event.target.checked })}
            />
          }
        />
        <FormHelperText>{Messages.dropIfExists.helper}</FormHelperText>
      </div>
      <AdvancedSection title={Messages.advanced} helperText={Messages.advancedHelper} defaultExpanded={tuningHasErrors}>
        <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' } }}>
          {PGCOPYDB_TUNING_FIELDS.map(({ field, min, max }) => (
            <TextField
              key={field}
              label={Messages.tuning[field].label}
              value={value.tuning[field]}
              disabled={disabled}
              onChange={(event) => onChange(updateTuning(value, field, event.target.value))}
              error={errors[field] !== undefined}
              helperText={errors[field] ?? Messages.tuning[field].helper}
              placeholder={Messages.summary.pgcopydbDefault}
              slotProps={{ htmlInput: { inputMode: 'numeric', min, max } }}
            />
          ))}
        </Box>
      </AdvancedSection>
    </Stack>
  );
};
