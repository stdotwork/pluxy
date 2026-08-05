import { spawn } from 'node:child_process';
import { readdir } from 'node:fs/promises';
import { basename, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const API_BASE = 'https://api.gitcode.com/api/v5';

function parseRepository(value) {
  const repository = String(value ?? '').trim();
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) {
    throw new Error(`Invalid GitCode repository: ${repository}`);
  }
  return repository.split('/');
}

function authenticatedUrl(path, token, query = {}) {
  const url = new URL(`${API_BASE}${path}`);
  url.searchParams.set('access_token', token);
  for (const [name, value] of Object.entries(query)) {
    url.searchParams.set(name, value);
  }
  return url;
}

async function apiRequest(path, {
  token,
  method = 'GET',
  body,
  query,
  expected = [200],
  fetchImpl = fetch,
}) {
  let response;
  try {
    response = await fetchImpl(authenticatedUrl(path, token, query), {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch (error) {
    throw new Error(`GitCode API request failed for ${path}: ${error instanceof Error ? error.message : error}`);
  }
  if (!expected.includes(response.status)) {
    const details = (await response.text()).slice(0, 500);
    throw new Error(`GitCode API ${method} ${path} failed: HTTP ${response.status}: ${details}`);
  }
  return response.status === 204 || response.status === 404 ? null : response.json();
}

export function uploadFileWithCurl({ url, headers, path }) {
  const args = [
    '--fail-with-body',
    '--silent',
    '--show-error',
    '--location',
    '--retry', '3',
    '--retry-delay', '5',
    '--retry-all-errors',
    '--connect-timeout', '30',
    '--max-time', '1800',
    '--request', 'PUT',
    '--upload-file', path,
    '--output', process.platform === 'win32' ? 'NUL' : '/dev/null',
  ];
  for (const [name, value] of Object.entries(headers)) {
    args.push('--header', `${name}: ${value}`);
  }
  args.push(url);

  return new Promise((resolveUpload, rejectUpload) => {
    const child = spawn('curl', args, { stdio: ['ignore', 'inherit', 'inherit'] });
    child.once('error', rejectUpload);
    child.once('exit', (code, signal) => {
      if (code === 0) {
        resolveUpload();
      } else {
        rejectUpload(new Error(`curl upload failed with ${signal ? `signal ${signal}` : `exit code ${code}`}`));
      }
    });
  });
}

export async function publishGitcodeRelease({
  directory,
  repository,
  token,
  tag,
  targetCommitish = 'main',
  prerelease = false,
  fetchImpl = fetch,
  uploadImpl = uploadFileWithCurl,
}) {
  const [owner, repo] = parseRepository(repository);
  if (!token) throw new Error('GITCODE_ACCESS_TOKEN is required');
  if (!/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(tag)) {
    throw new Error(`Invalid release tag: ${tag}`);
  }

  const encodedOwner = encodeURIComponent(owner);
  const encodedRepo = encodeURIComponent(repo);
  const encodedTag = encodeURIComponent(tag);
  const releaseByTagPath = `/repos/${encodedOwner}/${encodedRepo}/releases/tags/${encodedTag}`;
  let release;
  const existing = await apiRequest(releaseByTagPath, {
    token,
    expected: [200, 404],
    fetchImpl,
  });
  if (existing?.tag_name === tag) {
    release = existing;
  } else {
    release = await apiRequest(`/repos/${encodedOwner}/${encodedRepo}/releases`, {
      token,
      method: 'POST',
      body: {
        tag_name: tag,
        name: `EasyCLIProxyAPI ${tag}`,
        body: 'Mirror of the GitHub release for users who cannot access GitHub.',
        target_commitish: targetCommitish,
        release_status: prerelease ? 'pre' : 'latest',
      },
      fetchImpl,
    });
  }

  const existingAssets = new Set((release.assets ?? []).map((asset) => asset.name));
  const entries = (await readdir(directory, { withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .sort((left, right) => left.name.localeCompare(right.name));
  if (entries.length === 0) throw new Error(`No release artifacts found in ${directory}`);

  for (const entry of entries) {
    if (existingAssets.has(entry.name)) {
      console.log(`GitCode asset already exists, skipping: ${entry.name}`);
      continue;
    }
    const upload = await apiRequest(
      `/repos/${encodedOwner}/${encodedRepo}/releases/${encodedTag}/upload_url`,
      { token, query: { file_name: entry.name }, fetchImpl },
    );
    const descriptor = upload?.data ?? upload;
    const uploadUrl = descriptor?.upload_url ?? descriptor?.url;
    if (typeof uploadUrl !== 'string' || !uploadUrl.startsWith('https://')) {
      throw new Error(`GitCode returned an invalid upload URL for ${entry.name}`);
    }
    const rawHeaders = descriptor?.headers ?? descriptor?.upload_headers ?? descriptor?.header;
    const headers = Array.isArray(rawHeaders)
      ? Object.fromEntries(rawHeaders.map(({ key, name, value }) => [key ?? name, value]))
      : rawHeaders && typeof rawHeaders === 'object' ? rawHeaders : {};
    await uploadImpl({
      url: uploadUrl,
      headers,
      path: join(directory, entry.name),
    });
    console.log(`Uploaded GitCode asset: ${entry.name}`);
  }
}

async function main() {
  const directory = resolve(process.env.ARTIFACT_DIRECTORY ?? 'artifacts');
  await publishGitcodeRelease({
    directory,
    repository: process.env.GITCODE_REPOSITORY,
    token: process.env.GITCODE_ACCESS_TOKEN,
    tag: process.env.RELEASE_TAG,
    targetCommitish: process.env.GITCODE_TARGET_COMMITISH || 'main',
    prerelease: process.env.PRERELEASE === 'true' || process.env.RELEASE_TAG.includes('-'),
  });
  console.log(`Published ${basename(directory)} to GitCode ${process.env.GITCODE_REPOSITORY}`);
}

const entryPoint = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : '';
if (import.meta.url === entryPoint) {
  await main();
}
