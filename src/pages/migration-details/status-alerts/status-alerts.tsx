import { Alert, Stack } from '@mui/material';
import type { Migration } from '../../../api/migrations.types';
import { Messages } from '../migration-details.messages';
import type { PhaseAnnotation } from '../migration-details.types';

interface StatusAlertsProps {
  migration: Migration;
  annotation?: PhaseAnnotation;
}

export const StatusAlerts = ({ migration, annotation }: StatusAlertsProps) => {
  const showMessage = migration.message !== '' && migration.message !== migration.error;
  if (!annotation && !showMessage && migration.error === '') return null;

  return (
    <Stack spacing={1}>
      {annotation && (
        <Alert severity={annotation === 'failed' ? 'error' : 'warning'}>{Messages.annotations[annotation]}</Alert>
      )}
      {migration.error !== '' && <Alert severity="error">{migration.error}</Alert>}
      {showMessage && <Alert severity="info">{migration.message}</Alert>}
    </Stack>
  );
};
