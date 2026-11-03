import { Stack, Typography } from '@mui/material';
import type { Engine } from '../../../engines/engine.types';
import { Messages } from '../create-migration.messages';

interface OptionsStepProps {
  engine: Engine;
  options: unknown;
  onChange: (options: unknown) => void;
}

export const OptionsStep = ({ engine, options, onChange }: OptionsStepProps) => {
  const { OptionsForm } = engine;
  return (
    <Stack spacing={3}>
      <Typography variant="subtitle1">{Messages.options.heading}</Typography>
      <OptionsForm value={options} onChange={onChange} />
    </Stack>
  );
};
