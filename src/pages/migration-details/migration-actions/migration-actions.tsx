import { Box, Button } from '@mui/material';
import type { MigrationAction } from '../../../api/migrations.types';
import { Messages } from '../migration-details.messages';

interface MigrationActionsProps {
  allowedActions: MigrationAction[];
  disabled: boolean;
  onAction: (action: MigrationAction) => void;
}

const ACTION_ORDER: readonly MigrationAction[] = ['pause', 'resume', 'cutover', 'delete'];

export const MigrationActions = ({ allowedActions, disabled, onAction }: MigrationActionsProps) => (
  <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
    {ACTION_ORDER.filter((action) => allowedActions.includes(action)).map((action) => (
      <Button
        key={action}
        disabled={disabled}
        onClick={() => onAction(action)}
        variant={action === 'cutover' ? 'contained' : 'outlined'}
        color={action === 'delete' ? 'error' : 'primary'}
      >
        {Messages.actions[action]}
      </Button>
    ))}
  </Box>
);
