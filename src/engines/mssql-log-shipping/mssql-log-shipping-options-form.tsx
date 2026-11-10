import { Alert, FormControlLabel, FormHelperText, Stack, Switch, TextField } from '@mui/material';
import { AdvancedSection } from '../../components/advanced-section/advanced-section';
import { ChipListInput } from '../../components/chip-list-input/chip-list-input';
import type { EngineOptionsFormProps } from '../engine.types';
import { LOG_BACKUP_INTERVAL } from './mssql-log-shipping.constants';
import { Messages } from './mssql-log-shipping.messages';
import type { MssqlLogShippingOptionsDraft } from './mssql-log-shipping.types';
import { isValidDatabase, validateMssqlLogShippingOptions } from './mssql-log-shipping.utils';

type SwitchField = 'recreateDatabases' | 'copyLogins' | 'leaveSourceRestoring' | 'ignoreRecentLogBackups';

const SWITCHES: SwitchField[] = ['recreateDatabases', 'copyLogins', 'leaveSourceRestoring', 'ignoreRecentLogBackups'];

export const MssqlLogShippingOptionsForm = ({
  value,
  onChange,
  disabled,
}: EngineOptionsFormProps<MssqlLogShippingOptionsDraft>) => {
  const errors = validateMssqlLogShippingOptions(value);

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
      {SWITCHES.map((field) => (
        <div key={field}>
          <FormControlLabel
            label={Messages[field].label}
            control={
              <Switch
                checked={value[field]}
                disabled={disabled}
                onChange={(event) => onChange({ ...value, [field]: event.target.checked })}
              />
            }
          />
          <FormHelperText>{Messages[field].helper}</FormHelperText>
        </div>
      ))}
      <AdvancedSection
        title={Messages.advanced}
        helperText={Messages.advancedHelper}
        defaultExpanded={errors.logBackupIntervalSeconds !== undefined}
      >
        <TextField
          label={Messages.logBackupInterval.label}
          value={value.logBackupIntervalSeconds}
          disabled={disabled}
          onChange={(event) => onChange({ ...value, logBackupIntervalSeconds: event.target.value })}
          error={errors.logBackupIntervalSeconds !== undefined}
          helperText={errors.logBackupIntervalSeconds ?? Messages.logBackupInterval.helper}
          placeholder={String(LOG_BACKUP_INTERVAL.default)}
          slotProps={{
            htmlInput: { inputMode: 'numeric', min: LOG_BACKUP_INTERVAL.min, max: LOG_BACKUP_INTERVAL.max },
          }}
        />
      </AdvancedSection>
    </Stack>
  );
};
