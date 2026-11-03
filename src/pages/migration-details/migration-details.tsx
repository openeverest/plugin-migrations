import { useCallback } from 'react';
import { Alert, Box, Button, CircularProgress, Paper, Stack } from '@mui/material';
import { getMigration } from '../../api/migrations';
import type { Migration } from '../../api/migrations.types';
import type { NavigateFn } from '../../hooks/plugin-navigation.types';
import { usePolling } from '../../hooks/use-polling';
import { CutoverDialog } from './cutover-dialog/cutover-dialog';
import { DeleteDialog } from './delete-dialog/delete-dialog';
import { DetailsHeader } from './details-header/details-header';
import { EndpointsCard } from './endpoints-card/endpoints-card';
import { MigrationActions } from './migration-actions/migration-actions';
import { DETAILS_POLL_INTERVAL_MS } from './migration-details.constants';
import { Messages } from './migration-details.messages';
import { detailsSubtitle, phaseStepperState } from './migration-details.utils';
import { OptionsCard } from './options-card/options-card';
import { PhaseStepper } from './phase-stepper/phase-stepper';
import { ProgressCard } from './progress-card/progress-card';
import { StatusAlerts } from './status-alerts/status-alerts';
import { useMigrationActions } from './use-migration-actions';

interface MigrationDetailsProps {
  id: string;
  onNavigate: NavigateFn;
}

export const MigrationDetails = ({ id, onNavigate }: MigrationDetailsProps) => {
  const load = useCallback((signal: AbortSignal) => getMigration(id, signal), [id]);
  const { data: migration, error, refresh, setData } = usePolling(load, DETAILS_POLL_INTERVAL_MS, id);
  const goToList = useCallback(() => onNavigate({ kind: 'list' }), [onNavigate]);
  const onUpdated = useCallback((updated: Migration) => setData(updated), [setData]);
  const actions = useMigrationActions(id, { onUpdated, onDeleted: goToList });

  const loadError = error && (
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
  );

  if (!migration) {
    return (
      <Stack spacing={3}>
        <DetailsHeader name={id} onBack={goToList} />
        {loadError || (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
            <CircularProgress />
          </Box>
        )}
      </Stack>
    );
  }

  const stepper = phaseStepperState(migration);
  const busy = actions.pending !== undefined;

  return (
    <Stack spacing={3}>
      <DetailsHeader
        name={migration.name}
        phase={migration.phase}
        subtitle={detailsSubtitle(migration)}
        onBack={goToList}
        actions={
          <MigrationActions allowedActions={migration.allowedActions} disabled={busy} onAction={actions.request} />
        }
      />
      {loadError}
      {actions.error && <Alert severity="error">{Messages.actionError(actions.error)}</Alert>}

      <Paper variant="outlined" sx={{ p: 3 }}>
        <PhaseStepper state={stepper} />
      </Paper>
      <StatusAlerts migration={migration} annotation={stepper.annotation} />

      <Box sx={{ display: 'grid', gap: 3, gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' }, alignItems: 'start' }}>
        <ProgressCard migration={migration} />
        <Stack spacing={3}>
          <EndpointsCard migration={migration} />
          <OptionsCard migration={migration} />
        </Stack>
      </Box>

      <CutoverDialog
        open={actions.dialog === 'cutover'}
        busy={actions.pending === 'cutover'}
        onCancel={actions.closeDialog}
        onConfirm={() => void actions.run('cutover')}
      />
      <DeleteDialog
        open={actions.dialog === 'delete'}
        busy={actions.pending === 'delete'}
        migrationName={migration.name}
        active={migration.phase !== 'completed' && migration.phase !== 'failed'}
        onCancel={actions.closeDialog}
        onConfirm={() => void actions.run('delete')}
      />
    </Stack>
  );
};
