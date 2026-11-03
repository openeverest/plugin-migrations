import type { ReactNode } from 'react';
import { Box, Button, Typography } from '@mui/material';
import type { MigrationPhase } from '../../../api/migrations.types';
import { PhaseChip } from '../../../components/phase-chip/phase-chip';
import { Messages } from '../migration-details.messages';

interface DetailsHeaderProps {
  name: string;
  phase?: MigrationPhase;
  subtitle?: string;
  status?: ReactNode;
  actions?: ReactNode;
  onBack: () => void;
}

export const DetailsHeader = ({ name, phase, subtitle, status, actions, onBack }: DetailsHeaderProps) => (
  <Box>
    <Button size="small" onClick={onBack} sx={{ mb: 1, ml: -1 }}>
      {Messages.back}
    </Button>
    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2, flexWrap: 'wrap' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, flexWrap: 'wrap' }}>
        <Typography variant="h4" sx={{ wordBreak: 'break-all' }}>
          {name}
        </Typography>
        {phase && <PhaseChip phase={phase} size="medium" />}
      </Box>
      {actions}
    </Box>
    {(subtitle || status) && (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, mt: 0.5, flexWrap: 'wrap' }}>
        {subtitle && (
          <Typography variant="body2" color="text.secondary">
            {subtitle}
          </Typography>
        )}
        {status}
      </Box>
    )}
  </Box>
);
