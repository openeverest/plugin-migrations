import { CircularProgress, FormControl, FormHelperText, InputLabel, MenuItem, Select } from '@mui/material';
import { useEffect, useId, useState } from 'react';
import { fetchBackupStorages } from '../../../api/backup-storages';
import type { BackupStorageSummary } from '../../../api/backup-storages.types';
import type { InstanceSummary } from '../../../api/instances.types';
import { errorMessage } from '../../../utils/error.utils';
import { Messages } from '../create-migration.messages';

interface BackupStorageSelectProps {
  target: InstanceSummary;
  value: string;
  onChange: (name: string) => void;
}

interface StoragesState {
  storages?: BackupStorageSummary[];
  error?: string;
}

/** Picks an S3 backup storage in the target instance's namespace. */
export const BackupStorageSelect = ({ target, value, onChange }: BackupStorageSelectProps) => {
  const labelId = useId();
  const [state, setState] = useState<StoragesState>({});
  const { k8sCluster, namespace } = target;

  useEffect(() => {
    const controller = new AbortController();
    setState({});
    fetchBackupStorages(k8sCluster, namespace, controller.signal)
      .then((storages) => {
        if (!controller.signal.aborted) setState({ storages: storages.filter((storage) => storage.type === 's3') });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setState({ error: errorMessage(error) });
      });
    return () => controller.abort();
  }, [k8sCluster, namespace]);

  const storages = state.storages ?? [];
  const loading = state.storages === undefined && state.error === undefined;
  const empty = !loading && storages.length === 0;
  const selectedValue = storages.some((storage) => storage.name === value) ? value : '';
  let helper = Messages.target.backupStorageHelper;
  if (state.error) helper = Messages.target.backupStorageError(state.error);
  else if (empty) helper = Messages.target.noBackupStorages;

  return (
    <FormControl required disabled={loading || empty} error={Boolean(state.error)} sx={{ maxWidth: 480 }} fullWidth>
      <InputLabel id={labelId}>{Messages.target.backupStorageLabel}</InputLabel>
      <Select
        labelId={labelId}
        label={Messages.target.backupStorageLabel}
        value={selectedValue}
        onChange={(event) => onChange(event.target.value)}
        endAdornment={loading ? <CircularProgress size={20} sx={{ mr: 3 }} /> : undefined}
      >
        {storages.map((storage) => (
          <MenuItem key={storage.name} value={storage.name}>
            {storage.bucket ? `${storage.name} (${storage.bucket})` : storage.name}
          </MenuItem>
        ))}
      </Select>
      <FormHelperText>{helper}</FormHelperText>
    </FormControl>
  );
};
