import type { Migration } from '../../api/migrations.types';
import { progressPercent } from '../../utils/format.utils';
import type { ProgressSummary } from './migrations-page.types';

export function summarizeProgress({ phase, progress }: Migration): ProgressSummary {
  if (phase === 'full-load') {
    return {
      kind: 'copy',
      bytesCopied: progress.bytesCopied,
      bytesTotal: progress.bytesTotal,
      percent: progressPercent(progress.bytesCopied, progress.bytesTotal),
    };
  }
  if (phase === 'catching-up' || phase === 'replicating') {
    return { kind: 'lag', lagSeconds: progress.lagSeconds };
  }
  return { kind: 'none' };
}
