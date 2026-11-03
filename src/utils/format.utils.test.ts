import { describe, expect, it } from 'vitest';
import { formatBytes, formatDuration, formatPercent, formatRelativeTime, progressPercent } from './format.utils';

describe('formatBytes', () => {
  it('uses binary units with sensible precision', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KiB');
    expect(formatBytes(268_435_456)).toBe('256 MiB');
    expect(formatBytes(1_073_741_824)).toBe('1 GiB');
    expect(formatBytes(15.25 * 1024 ** 3)).toBe('15.3 GiB');
  });

  it('treats invalid and negative values as zero', () => {
    expect(formatBytes(-5)).toBe('0 B');
    expect(formatBytes(Number.NaN)).toBe('0 B');
  });
});

describe('progressPercent', () => {
  it('computes and clamps the ratio', () => {
    expect(progressPercent(268_435_456, 1_073_741_824)).toBe(25);
    expect(progressPercent(2_000, 1_000)).toBe(100);
    expect(progressPercent(-1, 1_000)).toBe(0);
  });

  it('is undefined when the total is unknown', () => {
    expect(progressPercent(10, 0)).toBeUndefined();
    expect(progressPercent(10, Number.NaN)).toBeUndefined();
  });
});

describe('formatPercent', () => {
  it('shows one decimal only for small values', () => {
    expect(formatPercent(0)).toBe('0%');
    expect(formatPercent(2.345)).toBe('2.3%');
    expect(formatPercent(25.9)).toBe('25%');
    expect(formatPercent(100)).toBe('100%');
  });
});

describe('formatDuration', () => {
  it('formats compact durations', () => {
    expect(formatDuration(0)).toBe('0s');
    expect(formatDuration(0.4)).toBe('<1s');
    expect(formatDuration(42)).toBe('42s');
    expect(formatDuration(125)).toBe('2m 5s');
    expect(formatDuration(3_600)).toBe('1h');
    expect(formatDuration(3_720)).toBe('1h 2m');
    expect(formatDuration(90_000)).toBe('1d 1h');
  });

  it('rejects invalid values', () => {
    expect(formatDuration(-1)).toBe('—');
  });
});

describe('formatRelativeTime', () => {
  it('picks the largest fitting unit', () => {
    const now = new Date('2026-11-03T10:00:00Z');
    expect(formatRelativeTime('2026-11-03T09:55:00Z', now)).toMatch(/5 minutes ago/);
    expect(formatRelativeTime('2026-11-01T10:00:00Z', now)).toMatch(/2 days ago/);
  });

  it('handles missing or invalid timestamps', () => {
    expect(formatRelativeTime(undefined)).toBe('—');
    expect(formatRelativeTime('not-a-date')).toBe('—');
  });
});
