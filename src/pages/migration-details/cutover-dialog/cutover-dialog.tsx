import { useEffect, useState } from 'react';
import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  FormGroup,
} from '@mui/material';
import { Messages } from '../migration-details.messages';

interface CutoverDialogProps {
  open: boolean;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

const CHECKLIST = Messages.cutoverDialog.checklist;

export const CutoverDialog = ({ open, busy, onCancel, onConfirm }: CutoverDialogProps) => {
  const [checked, setChecked] = useState<boolean[]>(() => CHECKLIST.map(() => false));

  useEffect(() => {
    if (open) setChecked(CHECKLIST.map(() => false));
  }, [open]);

  const allChecked = checked.every(Boolean);

  return (
    <Dialog open={open} onClose={busy ? undefined : onCancel} maxWidth="sm" fullWidth>
      <DialogTitle>{Messages.cutoverDialog.title}</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>{Messages.cutoverDialog.description}</DialogContentText>
        <FormGroup>
          {CHECKLIST.map((item, index) => (
            <FormControlLabel
              key={item}
              label={item}
              control={
                <Checkbox
                  checked={checked[index]}
                  disabled={busy}
                  onChange={(event) =>
                    setChecked((current) => current.map((value, i) => (i === index ? event.target.checked : value)))
                  }
                />
              }
            />
          ))}
        </FormGroup>
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel} disabled={busy}>
          {Messages.cutoverDialog.cancel}
        </Button>
        <Button variant="contained" onClick={onConfirm} disabled={!allChecked || busy}>
          {Messages.cutoverDialog.confirm}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
