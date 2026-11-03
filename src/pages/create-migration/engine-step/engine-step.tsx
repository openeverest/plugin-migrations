import { Box, Stack, TextField, Typography } from '@mui/material';
import type { Engine } from '../../../engines/engine.types';
import { Messages } from '../create-migration.messages';
import type { CreateMigrationDraft, DraftErrors } from '../create-migration.types';
import { EngineCard } from './engine-card/engine-card';

interface EngineStepProps {
  draft: CreateMigrationDraft;
  engines: Engine[];
  errors: DraftErrors;
  onNameChange: (name: string) => void;
  onSelectEngine: (engine: Engine) => void;
}

export const EngineStep = ({ draft, engines, errors, onNameChange, onSelectEngine }: EngineStepProps) => {
  const nameError = draft.name === '' ? undefined : errors.name;

  return (
    <Stack spacing={3}>
      <TextField
        required
        label={Messages.name.label}
        value={draft.name}
        onChange={(event) => onNameChange(event.target.value)}
        error={nameError !== undefined}
        helperText={nameError ?? Messages.name.helper}
        slotProps={{ htmlInput: { autoComplete: 'off', spellCheck: false } }}
        sx={{ maxWidth: 480 }}
      />
      <Box>
        <Typography variant="subtitle1" gutterBottom>
          {Messages.engine.heading}
        </Typography>
        <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' } }}>
          {engines.map((engine) => (
            <EngineCard
              key={engine.id}
              engine={engine}
              selected={engine.id === draft.engineId}
              onSelect={onSelectEngine}
            />
          ))}
        </Box>
      </Box>
    </Stack>
  );
};
