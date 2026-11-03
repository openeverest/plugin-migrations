import { Alert, Box, Button, CircularProgress, Stack, Typography } from '@mui/material';
import { listMigrations } from '../../api/migrations';
import type { NavigateFn } from '../../hooks/plugin-navigation.types';
import { usePolling } from '../../hooks/use-polling';
import { EmptyState } from './empty-state/empty-state';
import { LIST_POLL_INTERVAL_MS } from './migrations-page.constants';
import { Messages } from './migrations-page.messages';
import { MigrationsTable } from './migrations-table/migrations-table';

interface MigrationsPageProps {
  onNavigate: NavigateFn;
}

export const MigrationsPage = ({ onNavigate }: MigrationsPageProps) => {
  const { data: migrations, error, refresh } = usePolling(listMigrations, LIST_POLL_INTERVAL_MS);
  const openCreate = () => onNavigate({ kind: 'create' });

  return (
    <Stack spacing={3}>
      <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 2, flexWrap: 'wrap' }}>
        <Box>
          <Typography variant="h4" gutterBottom>
            {Messages.title}
          </Typography>
          <Typography color="text.secondary">{Messages.subtitle}</Typography>
        </Box>
        <Button variant="contained" onClick={openCreate}>
          {Messages.create}
        </Button>
      </Box>

      {error && (
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              {Messages.retry}
            </Button>
          }
        >
          {Messages.loadError(error)}
        </Alert>
      )}

      {migrations === undefined && !error && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
          <CircularProgress />
        </Box>
      )}
      {migrations !== undefined && migrations.length === 0 && <EmptyState onCreate={openCreate} />}
      {migrations !== undefined && migrations.length > 0 && (
        <MigrationsTable migrations={migrations} onOpen={(id) => onNavigate({ kind: 'details', id })} />
      )}
    </Stack>
  );
};
