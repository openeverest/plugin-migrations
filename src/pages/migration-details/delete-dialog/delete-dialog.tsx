import { Alert, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle } from '@mui/material';
import { Messages } from '../migration-details.messages';

interface DeleteDialogProps {
  open: boolean;
  busy: boolean;
  migrationName: string;
  active: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

export const DeleteDialog = ({ open, busy, migrationName, active, onCancel, onConfirm }: DeleteDialogProps) => (
  <Dialog open={open} onClose={busy ? undefined : onCancel} maxWidth="sm" fullWidth>
    <DialogTitle>{Messages.deleteDialog.title}</DialogTitle>
    <DialogContent>
      <DialogContentText>{Messages.deleteDialog.description(migrationName)}</DialogContentText>
      {active && (
        <Alert severity="warning" sx={{ mt: 2 }}>
          {Messages.deleteDialog.activeWarning}
        </Alert>
      )}
    </DialogContent>
    <DialogActions>
      <Button onClick={onCancel} disabled={busy}>
        {Messages.deleteDialog.cancel}
      </Button>
      <Button variant="contained" color="error" onClick={onConfirm} disabled={busy}>
        {Messages.deleteDialog.confirm}
      </Button>
    </DialogActions>
  </Dialog>
);
