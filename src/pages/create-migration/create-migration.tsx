import { Alert, Box, Button, CircularProgress, Stack, Typography } from '@mui/material';
import type { NavigateFn } from '../../hooks/plugin-navigation.types';
import { CreateMigrationWizard } from './create-migration-wizard/create-migration-wizard';
import { Messages } from './create-migration.messages';
import { useWizardData } from './use-wizard-data';

interface CreateMigrationProps {
  k8sCluster: string;
  onNavigate: NavigateFn;
}

export const CreateMigration = ({ k8sCluster, onNavigate }: CreateMigrationProps) => {
  const { data, error, retry } = useWizardData(k8sCluster);
  const goToList = () => onNavigate({ kind: 'list' });

  return (
    <Stack spacing={3}>
      <Typography variant="h4">{Messages.title}</Typography>
      {error && (
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={retry}>
              {Messages.retry}
            </Button>
          }
        >
          {Messages.loadError(error)}
        </Alert>
      )}
      {!data && !error && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
          <CircularProgress />
        </Box>
      )}
      {data && data.engines.length === 0 && <Alert severity="warning">{Messages.noEngines}</Alert>}
      {data && data.engines.length > 0 && (
        <CreateMigrationWizard
          engines={data.engines}
          instances={data.instances}
          onCreated={(migration) => onNavigate({ kind: 'details', id: migration.id })}
          onCancel={goToList}
        />
      )}
    </Stack>
  );
};
