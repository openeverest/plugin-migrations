import { Box, LinearProgress, Typography } from '@mui/material';
import type { Migration } from '../../../api/migrations.types';
import { formatBytes, formatDuration } from '../../../utils/format.utils';
import { Messages } from '../migrations-page.messages';
import { summarizeProgress } from '../migrations-page.utils';

interface MigrationProgressCellProps {
  migration: Migration;
}

export const MigrationProgressCell = ({ migration }: MigrationProgressCellProps) => {
  const summary = summarizeProgress(migration);

  if (summary.kind === 'lag') {
    return <Typography variant="body2">{Messages.lag(formatDuration(summary.lagSeconds))}</Typography>;
  }
  if (summary.kind === 'none') {
    return (
      <Typography variant="body2" color="text.secondary">
        {Messages.noProgress}
      </Typography>
    );
  }
  return (
    <Box sx={{ minWidth: 140 }}>
      <LinearProgress
        variant={summary.percent === undefined ? 'indeterminate' : 'determinate'}
        value={summary.percent}
        sx={{ mb: 0.5 }}
      />
      <Typography variant="caption" color="text.secondary">
        {summary.bytesTotal > 0
          ? Messages.copied(formatBytes(summary.bytesCopied), formatBytes(summary.bytesTotal))
          : Messages.copiedUnknownTotal(formatBytes(summary.bytesCopied))}
      </Typography>
    </Box>
  );
};
