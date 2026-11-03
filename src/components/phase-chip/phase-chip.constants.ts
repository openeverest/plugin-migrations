import type { ChipProps } from '@mui/material';
import type { MigrationPhase } from '../../api/migrations.types';

export const PHASE_CHIP_COLORS: Record<MigrationPhase, ChipProps['color']> = {
  provisioning: 'default',
  starting: 'default',
  'full-load': 'default',
  'catching-up': 'default',
  replicating: 'info',
  paused: 'warning',
  finalizing: 'default',
  completed: 'success',
  failed: 'error',
  unknown: 'default',
};
