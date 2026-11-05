import { invoke } from '@tauri-apps/api/core';

export type CoreConnection = { mode: 'local' | 'remote'; managementOrigin: string; managementKey: string };
export type ConnectionTestResult = { connected: boolean; version: string | null; message: string };

export const getCoreConnection = () => invoke<CoreConnection>('get_core_connection');
export const saveCoreConnection = (connection: CoreConnection) => invoke<CoreConnection>('save_core_connection', { connection });
export const testCoreConnection = (connection: CoreConnection) => invoke<ConnectionTestResult>('test_core_connection', { connection });

const loopbackHosts = ['localhost', '127.0.0.1', '[::1]'];

function parseOrigin(value: string) {
  try { return new URL(value.trim()); } catch { return null; }
}

// Mirrors the native origin check so Save and Test are offered only for values the core will accept.
export function isValidRemoteOrigin(value: string) {
  const url = parseOrigin(value);
  return Boolean(url && ['http:', 'https:'].includes(url.protocol) && url.hostname
    && !url.username && !url.password && url.pathname === '/' && !url.search && !url.hash);
}

export function isInsecureRemoteOrigin({ mode, managementOrigin }: CoreConnection) {
  const url = mode === 'remote' ? parseOrigin(managementOrigin) : null;
  return url?.protocol === 'http:' && !loopbackHosts.includes(url.hostname);
}

export function isCompleteRemoteDraft(draft: CoreConnection) {
  return draft.mode === 'remote' && isValidRemoteOrigin(draft.managementOrigin) && Boolean(draft.managementKey);
}

export function connectionChanged(draft: CoreConnection, saved: CoreConnection) {
  return draft.mode !== saved.mode || (draft.mode === 'remote'
    && (draft.managementOrigin.trim() !== saved.managementOrigin || draft.managementKey !== saved.managementKey));
}

export function canSaveConnection(draft: CoreConnection, saved: CoreConnection, localCoreRunning: boolean) {
  if (!connectionChanged(draft, saved)) return false;
  if (draft.mode === 'local') return true;
  return isCompleteRemoteDraft(draft) && (saved.mode === 'remote' || !localCoreRunning);
}
