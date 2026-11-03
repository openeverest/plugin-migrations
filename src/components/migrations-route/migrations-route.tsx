import { Alert, Box } from '@mui/material';
import type { PluginRouteProps } from '@openeverest/plugin-sdk';
import { usePluginNavigation } from '../../hooks/use-plugin-navigation';
import { CreateMigration } from '../../pages/create-migration/create-migration';
import { MigrationDetails } from '../../pages/migration-details/migration-details';
import { MigrationsPage } from '../../pages/migrations-page/migrations-page';
import { basePath } from '../../plugin-api';
import { clusterFromBasePath } from '../../utils/cluster.utils';
import { PluginRoot } from '../plugin-root';
import { Messages } from './migrations-route.messages';

export const MigrationsRoute = ({ pluginName, subPath }: PluginRouteProps) => {
  const { view, navigate } = usePluginNavigation(pluginName, subPath);
  const k8sCluster = clusterFromBasePath(basePath);

  return (
    <PluginRoot>
      <Box sx={{ p: { xs: 2, md: 4 }, maxWidth: 1280 }}>
        {view.kind === 'list' && <MigrationsPage onNavigate={navigate} />}
        {view.kind === 'create' &&
          (k8sCluster ? (
            <CreateMigration k8sCluster={k8sCluster} onNavigate={navigate} />
          ) : (
            <Alert severity="error">{Messages.invalidCluster}</Alert>
          ))}
        {view.kind === 'details' && <MigrationDetails key={view.id} id={view.id} onNavigate={navigate} />}
      </Box>
    </PluginRoot>
  );
};
