import { Alert, FormControlLabel, FormHelperText, Stack, Switch, TextField } from '@mui/material';
import { AdvancedSection } from '../../components/advanced-section/advanced-section';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { COPY_THREADS } from './mysql-replication.constants';
import { Messages } from './mysql-replication.messages';
import type { MysqlReplicationOptionsDraft } from './mysql-replication.types';
import { isValidDatabase, isValidTableName, validateMysqlReplicationOptions } from './mysql-replication.utils';

export const MysqlReplicationOptionsForm = ({
  value,
  onChange,
  disabled,
}: EngineOptionsFormProps<MysqlReplicationOptionsDraft>) => {
  const errors = validateMysqlReplicationOptions(value);

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
      <AdvancedSection
        title={Messages.advanced}
        helperText={Messages.advancedHelper}
        defaultExpanded={errors.copyThreads !== undefined}
      >
        <TextField
          label={Messages.copyThreads.label}
          value={value.copyThreads}
          disabled={disabled}
          onChange={(event) => onChange({ ...value, copyThreads: event.target.value })}
          error={errors.copyThreads !== undefined}
          helperText={errors.copyThreads ?? Messages.copyThreads.helper}
          placeholder={String(COPY_THREADS.default)}
          slotProps={{ htmlInput: { inputMode: 'numeric', min: COPY_THREADS.min, max: COPY_THREADS.max } }}
        />
      </AdvancedSection>
    </Stack>
  );
};
