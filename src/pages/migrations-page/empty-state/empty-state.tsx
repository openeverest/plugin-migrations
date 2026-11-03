import { Button, Paper, Typography } from '@mui/material';
import { Messages } from '../migrations-page.messages';

interface EmptyStateProps {
  onCreate: () => void;
}

export const EmptyState = ({ onCreate }: EmptyStateProps) => (
  <Paper variant="outlined" sx={{ p: 6, textAlign: 'center' }}>
    <Typography variant="h6" gutterBottom>
      {Messages.emptyTitle}
    </Typography>
    <Typography color="text.secondary" sx={{ mb: 3, maxWidth: 520, mx: 'auto' }}>
      {Messages.emptyDescription}
    </Typography>
    <Button variant="contained" onClick={onCreate}>
      {Messages.create}
    </Button>
  </Paper>
);
