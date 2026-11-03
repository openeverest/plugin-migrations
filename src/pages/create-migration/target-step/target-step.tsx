import { Stack, Typography } from '@mui/material';
import type { InstanceSummary } from '../../../api/instances.types';
import { Messages } from '../create-migration.messages';
import { InstanceSelect } from '../instance-select/instance-select';

interface TargetStepProps {
  instances: InstanceSummary[];
  value: string;
  onChange: (key: string) => void;
}

export const TargetStep = ({ instances, value, onChange }: TargetStepProps) => (
  <Stack spacing={3}>
    <Typography variant="subtitle1">{Messages.target.heading}</Typography>
    <InstanceSelect label={Messages.target.instanceLabel} instances={instances} value={value} onChange={onChange} />
  </Stack>
);
