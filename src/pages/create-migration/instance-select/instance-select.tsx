import { FormControl, FormHelperText, InputLabel, MenuItem, Select } from '@mui/material';
import { useId } from 'react';
import type { InstanceSummary } from '../../../api/instances.types';
import { instanceKey, instanceLabel } from '../../../utils/endpoint.utils';
import { Messages } from '../create-migration.messages';

interface InstanceSelectProps {
  label: string;
  instances: InstanceSummary[];
  value: string;
  onChange: (key: string) => void;
}

export const InstanceSelect = ({ label, instances, value, onChange }: InstanceSelectProps) => {
  const labelId = useId();
  const empty = instances.length === 0;
  const selectedValue = instances.some((instance) => instanceKey(instance) === value) ? value : '';

  return (
    <FormControl required disabled={empty} sx={{ maxWidth: 480 }} fullWidth>
      <InputLabel id={labelId}>{label}</InputLabel>
      <Select labelId={labelId} label={label} value={selectedValue} onChange={(event) => onChange(event.target.value)}>
        {instances.map((instance) => (
          <MenuItem key={instanceKey(instance)} value={instanceKey(instance)}>
            {instanceLabel(instance)}
          </MenuItem>
        ))}
      </Select>
      {empty && <FormHelperText>{Messages.noInstances}</FormHelperText>}
    </FormControl>
  );
};
