import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { beforeAll, describe, expect, it } from 'vitest';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

function inspectPeerIdentity() {
  const output = execFileSync('go', ['run', './tests/fixtures/peer-identity-uri'], {
    cwd: root,
    env: { ...process.env, GOWORK: 'off' },
    encoding: 'utf8',
  });
  return JSON.parse(output) as Record<string, boolean>;
}

describe('URI-only SDK peer identity', () => {
  let result: ReturnType<typeof inspectPeerIdentity>;

  beforeAll(() => {
    result = inspectPeerIdentity();
  }, 30000);

  it('accepts a URI-only identity for a Core-to-plugin client', () => {
    expect(result.uriOnlyIdentityValid).toBe(true);
    expect(result.uriOnlyClientCreated).toBe(true);
  });

  it('requires exact URI equality and never treats an empty common name as identity', () => {
    expect(result.exactUriMatches).toBe(true);
    expect(result.wrongUriRejected).toBe(true);
    expect(result.emptyPresentedIdentityRejected).toBe(true);
  });

  it('continues to reject absent, malformed, or wildcard expected identities', () => {
    expect(result.emptyExpectedIdentityRejected).toBe(true);
    expect(result.malformedUriRejected).toBe(true);
    expect(result.wildcardClientRejected).toBe(true);
  });
});
