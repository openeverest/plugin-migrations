import { describe, expect, it } from 'vitest';
import type { EngineDescriptor } from '../api/status.types';
import { ENGINES } from './index';
import { availableEngines, summarizeUnknownOptions } from './engines.utils';

const descriptor = (id: string): EngineDescriptor => ({
  id,
  displayName: id,
  sourceDatabaseTypes: [],
  targetDatabaseTypes: [],
  targetProviders: [],
});

describe('availableEngines', () => {
  it('offers only engines known to both the UI registry and the backend', () => {
    expect(availableEngines(ENGINES, [descriptor('pcsm'), descriptor('future-engine')]).map((e) => e.id)).toEqual([
      'pcsm',
    ]);
    expect(availableEngines(ENGINES, [descriptor('future-engine')])).toEqual([]);
    expect(availableEngines(ENGINES, [])).toEqual([]);
  });
});

describe('summarizeUnknownOptions', () => {
  it('renders raw option values for engines the UI does not know', () => {
    expect(summarizeUnknownOptions({ a: 'x', b: [1, 2], c: '' })).toEqual([
      { label: 'a', value: 'x' },
      { label: 'b', value: '[1,2]' },
      { label: 'c', value: '—' },
    ]);
    expect(summarizeUnknownOptions(null)).toEqual([]);
  });
});
