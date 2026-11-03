import { useMemo, useState } from 'react';
import type { InstanceSummary } from '../../api/instances.types';
import { createMigration } from '../../api/migrations';
import type { Migration } from '../../api/migrations.types';
import type { Engine } from '../../engines/engine.types';
import { errorMessage } from '../../utils/error.utils';
import { WIZARD_STEPS } from './create-migration.constants';
import type { CreateMigrationDraft } from './create-migration.types';
import {
  buildCreateRequest,
  buildWizardContext,
  createInitialDraft,
  selectEngine,
  validateStep,
  withSourceInstance,
} from './create-migration.utils';

export function useCreateMigrationWizard(
  engines: Engine[],
  instances: InstanceSummary[],
  onCreated: (migration: Migration) => void,
) {
  const [draft, setDraft] = useState(() => createInitialDraft(engines));
  const [activeIndex, setActiveIndex] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string>();

  const context = useMemo(() => buildWizardContext(draft, engines, instances), [draft, engines, instances]);
  const step = WIZARD_STEPS[activeIndex];
  const errors = validateStep(step, draft, context);
  const canProceed = Object.keys(errors).length === 0;
  const isLastStep = activeIndex === WIZARD_STEPS.length - 1;

  const updateDraft = (patch: Partial<CreateMigrationDraft>) => setDraft((current) => ({ ...current, ...patch }));

  const chooseEngine = (engine: Engine) => setDraft((current) => selectEngine(current, engine));

  const chooseSourceInstance = (key: string) => setDraft((current) => withSourceInstance(current, key));

  const next = () => {
    if (canProceed && !isLastStep) setActiveIndex(activeIndex + 1);
  };

  const back = () => {
    setSubmitError(undefined);
    setActiveIndex(Math.max(0, activeIndex - 1));
  };

  const submit = async () => {
    const request = buildCreateRequest(draft, context, WIZARD_STEPS);
    if (!request || submitting) return;
    setSubmitting(true);
    setSubmitError(undefined);
    try {
      onCreated(await createMigration(request));
    } catch (error) {
      setSubmitError(errorMessage(error));
      setSubmitting(false);
    }
  };

  return {
    draft,
    context,
    step,
    activeIndex,
    errors,
    canProceed,
    isLastStep,
    submitting,
    submitError,
    updateDraft,
    chooseEngine,
    chooseSourceInstance,
    next,
    back,
    submit,
  };
}
