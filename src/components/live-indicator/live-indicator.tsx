import { Box, Tooltip, Typography } from '@mui/material';
import { Messages } from './live-indicator.messages';

interface LiveIndicatorProps {
  lastUpdatedAt?: Date;
  // True when the latest refresh failed, so the data on screen may be outdated.
  stale: boolean;
}

export const LiveIndicator = ({ lastUpdatedAt, stale }: LiveIndicatorProps) => {
  if (!lastUpdatedAt) return null;
  const time = lastUpdatedAt.toLocaleTimeString();

  return (
    <Tooltip title={Messages.updatedAt(time)}>
      <Box sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.75 }}>
        <Box sx={{ width: 8, height: 8, borderRadius: '50%', bgcolor: stale ? 'warning.main' : 'success.main' }} />
        <Typography variant="body2" color={stale ? 'warning.main' : 'text.secondary'}>
          {stale ? Messages.lastUpdated(time) : Messages.live}
        </Typography>
      </Box>
    </Tooltip>
  );
};
