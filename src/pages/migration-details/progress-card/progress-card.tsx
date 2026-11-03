import { Box, Card, CardContent, Chip, LinearProgress, Typography } from '@mui/material';
import type { Migration } from '../../../api/migrations.types';
import { SummaryList } from '../../../components/summary-list/summary-list';
import {
  formatBytes,
  formatCount,
  formatDateTime,
  formatDuration,
  formatPercent,
  progressPercent,
} from '../../../utils/format.utils';
import { Messages } from '../migration-details.messages';
import { isChangeReplicationPhase } from '../migration-details.utils';

interface ProgressCardProps {
  migration: Migration;
}

export const ProgressCard = ({ migration }: ProgressCardProps) => {
  const { progress } = migration;
  const percent = progressPercent(progress.bytesCopied, progress.bytesTotal);
  const copied =
    percent === undefined
      ? Messages.progress.copiedUnknownTotal(formatBytes(progress.bytesCopied))
      : Messages.progress.copied(formatBytes(progress.bytesCopied), formatBytes(progress.bytesTotal), formatPercent(percent));

  return (
    <Card variant="outlined">
      <CardContent>
        <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2, mb: 2 }}>
          <Typography variant="h6">{Messages.progress.title}</Typography>
          {progress.readyForCutover && <Chip color="success" size="small" label={Messages.progress.readyForCutover} />}
        </Box>
        <Typography variant="body2" color="text.secondary">
          {Messages.progress.dataCopied}
        </Typography>
        <LinearProgress variant="determinate" value={percent ?? 0} sx={{ my: 1 }} />
        <Typography variant="body2" sx={{ mb: 2 }}>
          {copied}
        </Typography>
        <SummaryList
          items={[
            {
              label: Messages.progress.lag,
              value: isChangeReplicationPhase(migration)
                ? formatDuration(progress.lagSeconds)
                : Messages.progress.notApplicable,
            },
            { label: Messages.progress.eventsApplied, value: formatCount(progress.eventsApplied) },
            { label: Messages.progress.lastReplicatedAt, value: formatDateTime(progress.lastReplicatedAt) },
          ]}
        />
      </CardContent>
    </Card>
  );
};
