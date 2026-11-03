import { useCallback, useEffect, useRef, useState } from 'react';
import { cutoverMigration, deleteMigration, pauseMigration, resumeMigration } from '../../api/migrations';
import type { Migration, MigrationAction, WorkerAction } from '../../api/migrations.types';
import { errorMessage } from '../../utils/error.utils';

const WORKER_ACTION_HANDLERS: Record<WorkerAction, (id: string) => Promise<Migration>> = {
  pause: pauseMigration,
  resume: resumeMigration,
  cutover: cutoverMigration,
};

export type ConfirmDialog = 'cutover' | 'delete';

interface MigrationActionCallbacks {
  onUpdated: (migration: Migration) => void;
  onDeleted: () => void;
}

export function useMigrationActions(id: string, { onUpdated, onDeleted }: MigrationActionCallbacks) {
  const [pending, setPending] = useState<MigrationAction>();
  const [error, setError] = useState<string>();
  const [dialog, setDialog] = useState<ConfirmDialog>();
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    setError(undefined);
    setDialog(undefined);
  }, [id]);

  const run = useCallback(
    async (action: MigrationAction) => {
      setPending(action);
      setError(undefined);
      try {
        if (action === 'delete') {
          await deleteMigration(id);
          if (mounted.current) onDeleted();
          return;
        }
        const updated = await WORKER_ACTION_HANDLERS[action](id);
        if (!mounted.current) return;
        onUpdated(updated);
        setDialog(undefined);
      } catch (err) {
        if (!mounted.current) return;
        setError(errorMessage(err));
        setDialog(undefined);
      } finally {
        if (mounted.current) setPending(undefined);
      }
    },
    [id, onDeleted, onUpdated],
  );

  // Cutover and delete are confirmed in a dialog first; pause/resume run directly.
  const request = useCallback(
    (action: MigrationAction) => {
      if (action === 'cutover' || action === 'delete') setDialog(action);
      else void run(action);
    },
    [run],
  );

  const closeDialog = useCallback(() => setDialog(undefined), []);

  return { pending, error, dialog, request, run, closeDialog };
}
