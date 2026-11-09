import { Alert, FormControlLabel, FormHelperText, Stack, Switch, TextField } from '@mui/material';
import { AdvancedSection } from '../../components/advanced-section/advanced-section';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { COPY_THREADS, STORAGE_GIB, SYNC_THREADS } from './tidb-dm.constants';
import { Messages } from './tidb-dm.messages';
import type { TidbDmOptionsDraft } from './tidb-dm.types';
import { isValidDatabase, isValidTableName, validateTidbDmOptions } from './tidb-dm.utils';

export const TidbDmOptionsForm = ({ value, onChange, disabled }: EngineOptionsFormProps<TidbDmOptionsDraft>) => {
  const errors = validateTidbDmOptions(value);
  const numberField = (
    field: 'storageGiB' | 'copyThreads' | 'syncThreads',
    messages: { label: string; helper: string },
    range: { min: number; max: number; default: number },
  ) => (
    <TextField
      label={messages.label}
      value={value[field]}
      disabled={disabled}
      onChange={(event) => onChange({ ...value, [field]: event.target.value })}
      error={errors[field] !== undefined}
      helperText={errors[field] ?? messages.helper}
      placeholder={String(range.default)}
      slotProps={{ htmlInput: { inputMode: 'numeric', min: range.min, max: range.max } }}
    />
  );

  return (
    <Stack spacing={3}>
      <Alert severity="info">{Messages.notes}</Alert>
      <ChipListInput
        label={Messages.databases.label}
        helperText={Messages.databases.helper}
        value={value.databases}
        isValidItem={isValidDatabase}
        error={errors.databases}
        disabled={disabled}
        onChange={(databases) => onChange({ ...value, databases })}
      />
      <ChipListInput
        label={Messages.excludeTables.label}
        helperText={Messages.excludeTables.helper}
        value={value.excludeTables}
        isValidItem={isValidTableName}
        error={errors.excludeTables}
        disabled={disabled}
        onChange={(excludeTables) => onChange({ ...value, excludeTables })}
      />
      <div>
        <FormControlLabel
          label={Messages.recreateDatabases.label}
          control={
            <Switch
              checked={value.recreateDatabases}
              disabled={disabled}
              onChange={(event) => onChange({ ...value, recreateDatabases: event.target.checked })}
            />
          }
        />
        <FormHelperText>{Messages.recreateDatabases.helper}</FormHelperText>
      </div>
      {numberField('storageGiB', Messages.storageGiB, STORAGE_GIB)}
      <AdvancedSection
        title={Messages.advanced}
        helperText={Messages.advancedHelper}
        defaultExpanded={errors.copyThreads !== undefined || errors.syncThreads !== undefined}
      >
        <Stack spacing={3}>
          {numberField('copyThreads', Messages.copyThreads, COPY_THREADS)}
          {numberField('syncThreads', Messages.syncThreads, SYNC_THREADS)}
        </Stack>
      </AdvancedSection>
    </Stack>
  );
};
