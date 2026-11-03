import { Alert, Box, Button, Paper, Stack, Step, StepLabel, Stepper } from '@mui/material';
import type { InstanceSummary } from '../../../api/instances.types';
import type { Migration } from '../../../api/migrations.types';
import type { Engine } from '../../../engines/engine.types';
import { WIZARD_STEPS } from '../create-migration.constants';
import { Messages } from '../create-migration.messages';
import { buildReviewOptions, buildReviewSummary } from '../create-migration.utils';
import { EngineStep } from '../engine-step/engine-step';
import { OptionsStep } from '../options-step/options-step';
import { ReviewStep } from '../review-step/review-step';
import { SourceStep } from '../source-step/source-step';
import { TargetStep } from '../target-step/target-step';
import { useCreateMigrationWizard } from '../use-create-migration-wizard';

interface CreateMigrationWizardProps {
  engines: Engine[];
  instances: InstanceSummary[];
  onCreated: (migration: Migration) => void;
  onCancel: () => void;
}

export const CreateMigrationWizard = ({ engines, instances, onCreated, onCancel }: CreateMigrationWizardProps) => {
  const wizard = useCreateMigrationWizard(engines, instances, onCreated);
  const { draft, context, step, errors } = wizard;
  const { engine } = context;

  return (
    <Stack spacing={3}>
      <Stepper activeStep={wizard.activeIndex} alternativeLabel>
        {WIZARD_STEPS.map((wizardStep) => (
          <Step key={wizardStep}>
            <StepLabel>{Messages.steps[wizardStep]}</StepLabel>
          </Step>
        ))}
      </Stepper>

      <Paper variant="outlined" sx={{ p: 3 }}>
        {step === 'engine' && (
          <EngineStep
            draft={draft}
            engines={engines}
            errors={errors}
            onNameChange={(name) => wizard.updateDraft({ name })}
            onSelectEngine={wizard.chooseEngine}
          />
        )}
        {step === 'source' && engine && (
          <SourceStep
            draft={draft}
            engine={engine}
            instances={context.sourceInstances}
            errors={errors}
            onChange={wizard.updateDraft}
            onSelectInstance={wizard.chooseSourceInstance}
          />
        )}
        {step === 'target' && (
          <TargetStep
            instances={context.targetInstances}
            value={draft.targetInstanceKey}
            onChange={(targetInstanceKey) => wizard.updateDraft({ targetInstanceKey })}
          />
        )}
        {step === 'options' && engine && (
          <OptionsStep engine={engine} options={draft.options} onChange={(options) => wizard.updateDraft({ options })} />
        )}
        {step === 'review' && (
          <ReviewStep summary={buildReviewSummary(draft, context)} options={buildReviewOptions(draft, engine)} />
        )}
      </Paper>

      {wizard.submitError && <Alert severity="error">{Messages.createError(wizard.submitError)}</Alert>}

      <Box sx={{ display: 'flex', justifyContent: 'space-between', gap: 2 }}>
        <Button onClick={onCancel} disabled={wizard.submitting}>
          {Messages.cancel}
        </Button>
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Button onClick={wizard.back} disabled={wizard.activeIndex === 0 || wizard.submitting}>
            {Messages.back}
          </Button>
          {wizard.isLastStep ? (
            <Button variant="contained" onClick={wizard.submit} disabled={wizard.submitting}>
              {wizard.submitting ? Messages.creating : Messages.create}
            </Button>
          ) : (
            <Button variant="contained" onClick={wizard.next} disabled={!wizard.canProceed}>
              {Messages.next}
            </Button>
          )}
        </Box>
      </Box>
    </Stack>
  );
};
