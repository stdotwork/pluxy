import { describe, expect, test } from 'bun:test';
import { canSaveConnection, isInsecureRemoteOrigin, isValidRemoteOrigin, type CoreConnection } from '../src/services/coreConnection';

const local: CoreConnection = { mode: 'local', managementOrigin: '', managementKey: '' };
const remote = (managementOrigin: string, managementKey = 'secret'): CoreConnection => ({ mode: 'remote', managementOrigin, managementKey });

describe('core connection settings', () => {
  test('warns only for remote HTTP origins outside loopback', () => {
    expect(isInsecureRemoteOrigin(remote('http://core.example:8317'))).toBe(true);
    expect(isInsecureRemoteOrigin(remote(' http://10.0.0.5:8317 '))).toBe(true);
    for (const origin of ['http://localhost:8317', 'http://127.0.0.1:8317', 'http://[::1]:8317', 'https://core.example', 'core.example', '']) {
      expect(isInsecureRemoteOrigin(remote(origin))).toBe(false);
    }
    expect(isInsecureRemoteOrigin({ ...local, managementOrigin: 'http://core.example' })).toBe(false);
  });

  test('accepts only an HTTP(S) origin without path, query or credentials', () => {
    for (const origin of ['https://core.example:8317', ' http://10.0.0.5:8317/ ', 'http://[::1]:8317']) expect(isValidRemoteOrigin(origin)).toBe(true);
    for (const origin of ['', 'core.example:8317', 'ftp://core.example', 'https://core.example/v8', 'https://core.example?x=1', 'https://user:pw@core.example', 'https://core.example#a']) {
      expect(isValidRemoteOrigin(origin)).toBe(false);
    }
  });

  test('requires a complete, valid and changed remote target before saving', () => {
    expect(canSaveConnection(remote(''), local, false)).toBe(false);
    expect(canSaveConnection(remote('https://core.example', ''), local, false)).toBe(false);
    expect(canSaveConnection(remote('https://core.example/path'), local, false)).toBe(false);
    expect(canSaveConnection(remote('https://core.example'), local, false)).toBe(true);
    expect(canSaveConnection(local, remote('https://core.example'), true)).toBe(true);
  });

  test('offers Save only when the draft differs from the saved connection', () => {
    expect(canSaveConnection(local, local, false)).toBe(false);
    expect(canSaveConnection({ ...local, managementOrigin: 'https://stale.example' }, { ...local, managementOrigin: 'https://kept.example' }, false)).toBe(false);
    expect(canSaveConnection(remote(' https://core.example '), remote('https://core.example'), false)).toBe(false);
    expect(canSaveConnection(remote('https://core.example', 'rotated'), remote('https://core.example'), false)).toBe(true);
  });

  test('blocks switching from a running local core but allows editing an existing remote target', () => {
    expect(canSaveConnection(remote('https://core.example'), local, true)).toBe(false);
    expect(canSaveConnection(remote('https://other.example'), remote('https://core.example'), true)).toBe(true);
  });
});
