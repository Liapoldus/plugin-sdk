import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

describe('published module identity', () => {
  it('uses the approved canonical Go import path', () => {
    const goMod = readFileSync(join(import.meta.dirname, '..', 'go.mod'), 'utf8');
    expect(goMod.split('\n', 1)[0]).toBe('module github.com/Liapoldus/plugin-sdk/v2');
  });
});
