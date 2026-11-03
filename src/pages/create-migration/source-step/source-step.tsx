import { FormControlLabel, Radio, RadioGroup, Stack, TextField, Typography } from '@mui/material';
import type { InstanceSummary } from '../../../api/instances.types';
import type { Engine } from '../../../engines/engine.types';
import { Messages } from '../create-migration.messages';
import type { CreateMigrationDraft, DraftErrors, SourceKind } from '../create-migration.types';
import { InstanceSelect } from '../instance-select/instance-select';

interface SourceStepProps {
  draft: CreateMigrationDraft;
  engine: Engine;
  instances: InstanceSummary[];
  errors: DraftErrors;
  onChange: (patch: Partial<CreateMigrationDraft>) => void;
  onSelectInstance: (key: string) => void;
}

function isSourceKind(value: string): value is SourceKind {
  return value === 'instance' || value === 'external';
}

export const SourceStep = ({ draft, engine, instances, errors, onChange, onSelectInstance }: SourceStepProps) => {
  const { externalSource } = engine;
  const uriError = draft.externalUri === '' ? undefined : errors.externalUri;

  return (
    <Stack spacing={3}>
      <Typography variant="subtitle1">{Messages.source.heading}</Typography>
      <RadioGroup
        row
        value={draft.sourceKind}
        onChange={(event) => {
          if (isSourceKind(event.target.value)) onChange({ sourceKind: event.target.value });
        }}
      >
        {externalSource && <FormControlLabel value="external" control={<Radio />} label={externalSource.label} />}
        <FormControlLabel value="instance" control={<Radio />} label={Messages.source.instance} />
      </RadioGroup>

      {draft.sourceKind === 'instance' && (
        <InstanceSelect
          label={Messages.source.instanceLabel}
          instances={instances}
          value={draft.sourceInstanceKey}
          onChange={onSelectInstance}
        />
      )}

      {draft.sourceKind === 'external' && externalSource && (
        <>
          <TextField
            required
            label={externalSource.uriLabel}
            placeholder={externalSource.uriPlaceholder}
            value={draft.externalUri}
            onChange={(event) => onChange({ externalUri: event.target.value })}
            error={uriError !== undefined}
            helperText={uriError ?? externalSource.uriHelperText}
            slotProps={{ htmlInput: { autoComplete: 'off', spellCheck: false } }}
          />
          <TextField
            multiline
            minRows={4}
            label={externalSource.caCertLabel}
            value={draft.caCert}
            onChange={(event) => onChange({ caCert: event.target.value })}
            helperText={externalSource.caCertHelperText}
            slotProps={{ htmlInput: { spellCheck: false, sx: { fontFamily: 'monospace' } } }}
          />
        </>
      )}
    </Stack>
  );
};
