import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, test } from 'bun:test';
import { publishGitcodeRelease } from '../scripts/publish-gitcode-release.mjs';

describe('GitCode release mirror', () => {
  test('creates a release and uploads compiled artifacts', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'easycli-gitcode-release-'));
    const calls: Array<{ url: string; method: string; body: unknown }> = [];
    const uploads: Array<{ url: string; path: string }> = [];
    try {
      await writeFile(join(directory, 'EasyCLIProxyAPI-v1.2.3-Windows-amd64.zip'), 'artifact');
      const fetchImpl = async (input: URL | RequestInfo, init: RequestInit = {}) => {
        const url = String(input);
        const method = init.method ?? 'GET';
        calls.push({ url, method, body: init.body });
        if (method === 'GET' && url.includes('/releases/tags/')) {
          return new Response('', { status: 404 });
        }
        if (method === 'POST' && url.includes('/releases?')) {
          return Response.json({ tag_name: 'v1.2.3', assets: [] });
        }
        if (method === 'GET' && url.includes('/upload_url?')) {
          return Response.json({
            upload_url: 'https://file-cdn.gitcode.com/presigned-upload',
            headers: { 'x-upload-token': 'signed' },
          });
        }
        return new Response('', { status: 500 });
      };

      await publishGitcodeRelease({
        directory,
        repository: 'mirror-owner/EasyCLIProxyAPI',
        token: 'secret-token',
        tag: 'v1.2.3',
        fetchImpl,
        uploadImpl: async ({ url, path }) => {
          uploads.push({ url, path });
        },
      });

      expect(calls.map(({ method }) => method)).toEqual(['GET', 'POST', 'GET']);
      expect(calls[0].url).toContain('/repos/mirror-owner/EasyCLIProxyAPI/releases/tags/v1.2.3');
      expect(calls[0].url).toContain('access_token=secret-token');
      expect(calls[2].url).toContain('file_name=EasyCLIProxyAPI-v1.2.3-Windows-amd64.zip');
      expect(uploads).toEqual([{
        url: 'https://file-cdn.gitcode.com/presigned-upload',
        path: join(directory, 'EasyCLIProxyAPI-v1.2.3-Windows-amd64.zip'),
      }]);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });

  test('continues when GitCode stores an asset but the upload response times out', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'easycli-gitcode-release-'));
    const artifactName = 'EasyCLIProxyAPI-v1.2.3-Windows-amd64.zip';
    let releaseRequests = 0;
    try {
      await writeFile(join(directory, artifactName), 'artifact');
      const fetchImpl = async (input: URL | RequestInfo) => {
        const url = String(input);
        if (url.includes('/releases/tags/')) {
          releaseRequests += 1;
          return Response.json({
            tag_name: 'v1.2.3',
            assets: releaseRequests === 1 ? [] : [{ name: artifactName }],
          });
        }
        if (url.includes('/upload_url?')) {
          return Response.json({ upload_url: 'https://file-cdn.gitcode.com/presigned-upload' });
        }
        return new Response('', { status: 500 });
      };

      await expect(publishGitcodeRelease({
        directory,
        repository: 'mirror-owner/EasyCLIProxyAPI',
        token: 'secret-token',
        tag: 'v1.2.3',
        fetchImpl,
        uploadImpl: async () => {
          throw new Error('upload response timed out');
        },
      })).resolves.toBeUndefined();

      expect(releaseRequests).toBe(2);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
});
