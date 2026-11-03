import { Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Tooltip, Typography, Paper } from '@mui/material';
import type { Migration } from '../../../api/migrations.types';
import { PhaseChip } from '../../../components/phase-chip/phase-chip';
import { engineLabel } from '../../../engines';
import { endpointLabel } from '../../../utils/endpoint.utils';
import { formatDateTime, formatRelativeTime } from '../../../utils/format.utils';
import { MigrationProgressCell } from '../migration-progress-cell/migration-progress-cell';
import { Messages } from '../migrations-page.messages';

interface MigrationsTableProps {
  migrations: Migration[];
  onOpen: (id: string) => void;
}

export const MigrationsTable = ({ migrations, onOpen }: MigrationsTableProps) => (
  <TableContainer component={Paper} variant="outlined">
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>{Messages.columns.name}</TableCell>
          <TableCell>{Messages.columns.engine}</TableCell>
          <TableCell>{Messages.columns.endpoints}</TableCell>
          <TableCell>{Messages.columns.phase}</TableCell>
          <TableCell>{Messages.columns.progress}</TableCell>
          <TableCell>{Messages.columns.created}</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {migrations.map((migration) => (
          <TableRow
            key={migration.id}
            hover
            tabIndex={0}
            onClick={() => onOpen(migration.id)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') onOpen(migration.id);
            }}
            sx={{ cursor: 'pointer' }}
          >
            <TableCell>
              <Typography variant="body2" sx={{ fontWeight: 'fontWeightMedium' }}>
                {migration.name}
              </Typography>
            </TableCell>
            <TableCell>{engineLabel(migration.engine)}</TableCell>
            <TableCell>{Messages.endpoints(endpointLabel(migration.source), endpointLabel(migration.target))}</TableCell>
            <TableCell>
              <PhaseChip phase={migration.phase} />
            </TableCell>
            <TableCell>
              <MigrationProgressCell migration={migration} />
            </TableCell>
            <TableCell>
              <Tooltip title={formatDateTime(migration.createdAt)}>
                <span>{formatRelativeTime(migration.createdAt)}</span>
              </Tooltip>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  </TableContainer>
);
