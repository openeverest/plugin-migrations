import { Step, StepLabel, Stepper } from '@mui/material';
import { PHASE_STEPS } from '../migration-details.constants';
import { Messages } from '../migration-details.messages';
import type { PhaseStepperState } from '../migration-details.types';

interface PhaseStepperProps {
  state: PhaseStepperState;
}

export const PhaseStepper = ({ state }: PhaseStepperProps) => (
  <Stepper activeStep={state.activeStep} alternativeLabel>
    {PHASE_STEPS.map((step, index) => (
      <Step key={step} completed={index < state.activeStep}>
        <StepLabel error={state.failedStep === index}>{Messages.steps[step]}</StepLabel>
      </Step>
    ))}
  </Stepper>
);
