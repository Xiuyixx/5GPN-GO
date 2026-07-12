import { describe, expect, it } from 'vitest';
import {
  backupFileSizeAllowed,
  MAX_BACKUP_BYTES,
  readStreamWithLimit,
} from './Backup';

describe('backup upload size guard', () => {
  it('accepts only non-empty compressed files up to 32 MiB', () => {
    expect(backupFileSizeAllowed(0)).toBe(false);
    expect(backupFileSizeAllowed(MAX_BACKUP_BYTES)).toBe(true);
    expect(backupFileSizeAllowed(MAX_BACKUP_BYTES + 1)).toBe(false);
  });

  it('cancels preview decompression as soon as the expanded limit is exceeded', async () => {
    let cancelled = false;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([1, 2, 3]));
        controller.enqueue(new Uint8Array([4, 5, 6]));
      },
      cancel() {
        cancelled = true;
      },
    });

    await expect(readStreamWithLimit(stream, 5)).rejects.toThrow('expanded backup preview exceeds 5 bytes');
    expect(cancelled).toBe(true);
  });

  it('joins decompressed chunks without using an unbounded arrayBuffer', async () => {
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([1, 2]));
        controller.enqueue(new Uint8Array([3]));
        controller.close();
      },
    });

    await expect(readStreamWithLimit(stream, 3)).resolves.toEqual(new Uint8Array([1, 2, 3]));
  });
});
