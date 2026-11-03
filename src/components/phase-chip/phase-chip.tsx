import { Chip } from '@mui/material';
import type { MigrationPhase } from '../../api/migrations.types';
import { PHASE_CHIP_COLORS } from './phase-chip.constants';
import { Messages } from './phase-chip.messages';

interface PhaseChipProps {
  phase: MigrationPhase;
  size?: 'small' | 'medium';
}

export const PhaseChip = ({ phase, size = 'small' }: PhaseChipProps) => (
  <Chip size={size} label={Messages.phases[phase]} color={PHASE_CHIP_COLORS[phase]} />
);
