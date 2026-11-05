import { Box, TextField } from '@mui/material';
import { AdvancedSection } from '../../../components/advanced-section/advanced-section';
import type { EngineOptionsErrors } from '../../engine.types';
import { PCSM_TUNING_FIELDS } from '../pcsm.constants';
import { Messages } from '../pcsm.messages';
import type { PcsmTuningField } from '../pcsm.types';

interface TuningOptionsProps {
  value: Record<PcsmTuningField, string>;
  errors: EngineOptionsErrors;
  onChange: (field: PcsmTuningField, value: string) => void;
  disabled?: boolean;
}

export const TuningOptions = ({ value, errors, onChange, disabled }: TuningOptionsProps) => {
  const hasErrors = PCSM_TUNING_FIELDS.some(({ field }) => errors[field] !== undefined);

  return (
    <AdvancedSection title={Messages.advanced} helperText={Messages.advancedHelper} defaultExpanded={hasErrors}>
      <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' } }}>
        {PCSM_TUNING_FIELDS.map(({ field, min, max }) => (
          <TextField
            key={field}
            label={Messages.tuning[field].label}
            value={value[field]}
            disabled={disabled}
            onChange={(event) => onChange(field, event.target.value)}
            error={errors[field] !== undefined}
            helperText={errors[field] ?? Messages.tuning[field].helper}
            placeholder={Messages.summary.pcsmDefault}
            slotProps={{ htmlInput: { inputMode: 'numeric', min, max } }}
          />
        ))}
      </Box>
    </AdvancedSection>
  );
};
