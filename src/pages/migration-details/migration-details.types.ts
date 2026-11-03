export type PhaseStep = 'provisioning' | 'fullLoad' | 'replication' | 'cutover' | 'completed';

export type PhaseAnnotation = 'paused' | 'failed' | 'unknown';

export interface PhaseStepperState {
  /** Index into PHASE_STEPS; PHASE_STEPS.length means every step is done. */
  activeStep: number;
  failedStep?: number;
  annotation?: PhaseAnnotation;
}
