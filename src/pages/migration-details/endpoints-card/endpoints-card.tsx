import { Card, CardContent, Typography } from '@mui/material';
import type { EndpointRef, Migration } from '../../../api/migrations.types';
import { SummaryList } from '../../../components/summary-list/summary-list';
import { endpointLabel, endpointTypeLabel } from '../../../utils/endpoint.utils';
import { Messages } from '../migration-details.messages';

interface EndpointsCardProps {
  migration: Migration;
}

function describeEndpoint(endpoint: EndpointRef): string {
  const described = Messages.endpoints.describe(endpointTypeLabel(endpoint), endpointLabel(endpoint));
  return endpoint.type === 'instance' && endpoint.provider
    ? Messages.endpoints.provider(described, endpoint.provider)
    : described;
}

export const EndpointsCard = ({ migration }: EndpointsCardProps) => (
  <Card variant="outlined">
    <CardContent>
      <Typography variant="h6" gutterBottom>
        {Messages.endpoints.title}
      </Typography>
      <SummaryList
        items={[
          { label: Messages.endpoints.source, value: describeEndpoint(migration.source) },
          { label: Messages.endpoints.target, value: describeEndpoint(migration.target) },
          ...(migration.backupStorage
            ? [{ label: Messages.endpoints.backupStorage, value: migration.backupStorage.name }]
            : []),
        ]}
      />
    </CardContent>
  </Card>
);
