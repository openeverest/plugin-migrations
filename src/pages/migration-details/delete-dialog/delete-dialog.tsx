import { Alert, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle } from '@mui/material';
import { Messages } from '../migration-details.messages';

interface DeleteDialogProps {
  open: boolean;
  busy: boolean;
  migrationName: string;
  active: boolean;
  error?: string;
  onCancel: () => void;
  onConfirm: () => void;
  onForce: () => void;
}

export const DeleteDialog = ({
  open,
  busy,
  migrationName,
  active,
  error,
  onCancel,
  onConfirm,
  onForce,
}: DeleteDialogProps) => (
  <Dialog open={open} onClose={busy ? undefined : onCancel} maxWidth="sm" fullWidth>
    <DialogTitle>{Messages.deleteDialog.title}</DialogTitle>
    <DialogContent>
      <DialogContentText>{Messages.deleteDialog.description(migrationName)}</DialogContentText>
      {active && (
        <Alert severity="warning" sx={{ mt: 2 }}>
          {Messages.deleteDialog.activeWarning}
        </Alert>
      )}
      {error && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {error}
          <br />
          {Messages.deleteDialog.forceHint}
        </Alert>
      )}
    </DialogContent>
    <DialogActions>
      <Button onClick={onCancel} disabled={busy}>
        {Messages.deleteDialog.cancel}
      </Button>
      {error && (
        <Button color="error" onClick={onForce} disabled={busy}>
          {Messages.deleteDialog.forceConfirm}
        </Button>
      )}
      <Button variant="contained" color="error" onClick={onConfirm} disabled={busy}>
        {Messages.deleteDialog.confirm}
      </Button>
    </DialogActions>
  </Dialog>
);
