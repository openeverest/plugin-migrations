import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PcsmTuningField } from '../pcsm.types';
import { TuningOptions } from './tuning-options';

const EMPTY: Record<PcsmTuningField, string> = {
  cloneNumParallelCollections: '',
  cloneNumReadWorkers: '',
  cloneNumInsertWorkers: '',
  replicationNumWorkers: '',
};

const summary = () => screen.getByRole('button', { name: /advanced/i });

afterEach(cleanup);

describe('TuningOptions', () => {
  it('starts collapsed with a visible expand indicator and expands on click', () => {
    render(<TuningOptions value={EMPTY} errors={{}} onChange={vi.fn()} />);

    expect(summary().getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByTestId('ExpandMoreIcon')).toBeTruthy();

    fireEvent.click(summary());
    expect(summary().getAttribute('aria-expanded')).toBe('true');
  });

  it('starts expanded when a tuning field has an error', () => {
    render(<TuningOptions value={EMPTY} errors={{ cloneNumReadWorkers: 'Enter 1–256.' }} onChange={vi.fn()} />);

    expect(summary().getAttribute('aria-expanded')).toBe('true');
  });
});
