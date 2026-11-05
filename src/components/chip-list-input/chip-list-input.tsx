import { useState } from 'react';
import { Autocomplete, Chip, TextField } from '@mui/material';
import { splitListInput } from './chip-list-input.utils';

interface ChipListInputProps {
  label: string;
  helperText: string;
  value: string[];
  onChange: (value: string[]) => void;
  /** Items failing this check are highlighted; the caller reports the error. */
  isValidItem: (item: string) => boolean;
  error?: string;
  disabled?: boolean;
}

/** Free-text list of short items (names, patterns) shown as chips. */
export const ChipListInput = ({ label, helperText, value, onChange, isValidItem, error, disabled }: ChipListInputProps) => {
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
      onChange={(_, next) => onChange(splitListInput(next))}
      renderValue={(values, getItemProps) =>
        values.map((item, index) => {
          const { key, ...itemProps } = getItemProps({ index });
          return (
            <Chip key={key} size="small" label={item} color={isValidItem(item) ? 'default' : 'error'} {...itemProps} />
          );
        })
      }
      renderInput={(params) => (
        <TextField {...params} label={label} error={Boolean(error)} helperText={error ?? helperText} />
      )}
    />
  );
};
