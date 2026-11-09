import { Stack, Typography } from '@mui/material';
import type { InstanceSummary } from '../../../api/instances.types';
import { BackupStorageSelect } from '../backup-storage-select/backup-storage-select';
import { Messages } from '../create-migration.messages';
import { InstanceSelect } from '../instance-select/instance-select';

interface TargetStepProps {
  instances: InstanceSummary[];
  value: string;
  onChange: (key: string) => void;
  /** Set when the engine stages data in a backup storage of the target's namespace. */
  backupStorage?: {
    target: InstanceSummary | undefined;
    value: string;
    onChange: (name: string) => void;
  };
}

export const TargetStep = ({ instances, value, onChange, backupStorage }: TargetStepProps) => (
  <Stack spacing={3}>
    <Typography variant="subtitle1">{Messages.target.heading}</Typography>
    <InstanceSelect label={Messages.target.instanceLabel} instances={instances} value={value} onChange={onChange} />
    {backupStorage?.target && (
      <BackupStorageSelect target={backupStorage.target} value={backupStorage.value} onChange={backupStorage.onChange} />
    )}
  </Stack>
);
