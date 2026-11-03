import { Card, CardContent, Typography } from '@mui/material';
import type { Migration } from '../../../api/migrations.types';
import { SummaryList } from '../../../components/summary-list/summary-list';
import { summarizeEngineOptions } from '../../../engines';
import { Messages } from '../migration-details.messages';

interface OptionsCardProps {
  migration: Migration;
}

export const OptionsCard = ({ migration }: OptionsCardProps) => {
  const items = summarizeEngineOptions(migration.engine, migration.options);
  return (
    <Card variant="outlined">
      <CardContent>
        <Typography variant="h6" gutterBottom>
          {Messages.options.title}
        </Typography>
        {items.length > 0 ? (
          <SummaryList items={items} />
        ) : (
          <Typography variant="body2" color="text.secondary">
            {Messages.options.empty}
          </Typography>
        )}
      </CardContent>
    </Card>
  );
};
