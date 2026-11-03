import { useState } from 'react';
import { Autocomplete, Chip, TextField } from '@mui/material';
import { isValidNamespace, splitNamespaceInput } from '../pcsm.utils';

interface NamespaceListInputProps {
  label: string;
  helperText: string;
  value: string[];
  onChange: (value: string[]) => void;
  error?: string;
  disabled?: boolean;
}

export const NamespaceListInput = ({ label, helperText, value, onChange, error, disabled }: NamespaceListInputProps) => {
  const [inputValue, setInputValue] = useState('');

  return (
    <Autocomplete<string, true, false, true>
      multiple
      freeSolo
      autoSelect
      options={[]}
      value={value}
      inputValue={inputValue}
      disabled={disabled}
      onInputChange={(_, next) => setInputValue(next)}
      onChange={(_, next) => onChange(splitNamespaceInput(next))}
      renderValue={(values, getItemProps) =>
        values.map((namespace, index) => {
          const { key, ...itemProps } = getItemProps({ index });
          return (
            <Chip
              key={key}
              size="small"
              label={namespace}
              color={isValidNamespace(namespace) ? 'default' : 'error'}
              {...itemProps}
            />
          );
        })
      }
      renderInput={(params) => (
        <TextField {...params} label={label} error={Boolean(error)} helperText={error ?? helperText} />
      )}
    />
  );
};
