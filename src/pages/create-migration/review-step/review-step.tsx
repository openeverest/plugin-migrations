import { Card, CardContent, Stack, Typography } from '@mui/material';
import { SummaryList } from '../../../components/summary-list/summary-list';
import type { OptionSummaryItem } from '../../../engines/engine.types';
import { Messages } from '../create-migration.messages';

interface ReviewStepProps {
  summary: OptionSummaryItem[];
  options: OptionSummaryItem[];
}

export const ReviewStep = ({ summary, options }: ReviewStepProps) => (
  <Stack spacing={2}>
    <Card variant="outlined">
      <CardContent>
        <Typography variant="subtitle1" gutterBottom>
          {Messages.review.settings}
        </Typography>
        <SummaryList items={summary} />
      </CardContent>
    </Card>
    <Card variant="outlined">
      <CardContent>
        <Typography variant="subtitle1" gutterBottom>
          {Messages.review.options}
        </Typography>
        <SummaryList items={options} />
      </CardContent>
    </Card>
  </Stack>
);
