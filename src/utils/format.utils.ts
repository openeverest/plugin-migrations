import { Messages } from './format.messages';

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), BYTE_UNITS.length - 1);
  const value = bytes / 1024 ** exponent;
  const digits = exponent === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2;
  return `${Number(value.toFixed(digits))} ${BYTE_UNITS[exponent]}`;
}

export function progressPercent(done: number, total: number): number | undefined {
  if (!Number.isFinite(done) || !Number.isFinite(total) || total <= 0) return undefined;
  return Math.min(100, Math.max(0, (done / total) * 100));
}

export function formatPercent(percent: number): string {
  return `${percent >= 10 || percent === 0 ? Math.floor(percent) : percent.toFixed(1)}%`;
}

export function formatDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return Messages.notAvailable;
  if (totalSeconds > 0 && totalSeconds < 1) return Messages.lessThanASecond;
  const seconds = Math.floor(totalSeconds);
  const days = Math.floor(seconds / 86_400);
  const hours = Math.floor((seconds % 86_400) / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  const rest = seconds % 60;
  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  if (minutes > 0) return rest > 0 ? `${minutes}m ${rest}s` : `${minutes}m`;
  return `${rest}s`;
}

export function formatCount(value: number): string {
  return Number.isFinite(value) ? value.toLocaleString() : Messages.notAvailable;
}

function parseTimestamp(value: string | undefined): Date | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

export function formatDateTime(value: string | undefined): string {
  return parseTimestamp(value)?.toLocaleString() ?? Messages.notAvailable;
}

const RELATIVE_UNITS: ReadonlyArray<[Intl.RelativeTimeFormatUnit, number]> = [
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
  ['second', 1],
];

export function formatRelativeTime(value: string | undefined, now: Date = new Date()): string {
  const date = parseTimestamp(value);
  if (!date) return Messages.notAvailable;
  const diffSeconds = (date.getTime() - now.getTime()) / 1000;
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  for (const [unit, unitSeconds] of RELATIVE_UNITS) {
    if (Math.abs(diffSeconds) >= unitSeconds || unit === 'second') {
      return formatter.format(Math.round(diffSeconds / unitSeconds), unit);
    }
  }
  return Messages.notAvailable;
}
