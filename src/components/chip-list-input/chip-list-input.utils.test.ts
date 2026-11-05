import { describe, expect, it } from 'vitest';
import { splitListInput } from './chip-list-input.utils';

describe('splitListInput', () => {
  it('splits pasted lists on commas and whitespace and removes duplicates', () => {
    expect(splitListInput(['shop.*', 'a, b.c  d', 'shop.*'])).toEqual(['shop.*', 'a', 'b.c', 'd']);
  });
});
