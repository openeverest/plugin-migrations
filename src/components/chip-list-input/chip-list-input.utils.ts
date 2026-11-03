/** Splits pasted input such as "a, b.c d" into unique, non-empty items. */
export function splitListInput(values: readonly string[]): string[] {
  const entries = values.flatMap((value) => value.split(/[\s,]+/)).filter((value) => value !== '');
  return [...new Set(entries)];
}
