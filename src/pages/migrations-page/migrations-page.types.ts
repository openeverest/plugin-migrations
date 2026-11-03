export type ProgressSummary =
  | { kind: 'copy'; bytesCopied: number; bytesTotal: number; percent?: number }
  | { kind: 'lag'; lagSeconds: number }
  | { kind: 'none' };
