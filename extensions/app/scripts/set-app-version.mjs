import { spawnSync } from 'node:child_process';
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  parseCargoPackageVersion,
  setCargoLockPackageVersion,
  setCargoPackageVersion,
  validateAppVersion,
} from './app-version.mjs';

const requestedVersion = process.argv[2];
if (!requestedVersion) {
  throw new Error('Usage: node scripts/set-app-version.mjs <semver>');
}

const version = validateAppVersion(requestedVersion, 'requested app version');
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const manifest = join(root, 'src-tauri', 'Cargo.toml');
const lockfile = join(root, 'src-tauri', 'Cargo.lock');
const contents = await readFile(manifest, 'utf8');
const lockContents = await readFile(lockfile, 'utf8');
const previousVersion = parseCargoPackageVersion(contents, manifest);

if (previousVersion !== version) {
  await Promise.all([
    writeFile(manifest, setCargoPackageVersion(contents, version, manifest)),
    writeFile(lockfile, setCargoLockPackageVersion(lockContents, 'cpa-gui', version, lockfile)),
  ]);
}

const cargo = process.platform === 'win32' ? 'cargo.exe' : 'cargo';
const metadata = spawnSync(cargo, [
  'metadata',
  '--manifest-path', manifest,
  '--no-deps',
  '--format-version', '1',
], { stdio: 'ignore' });

if (metadata.error) throw metadata.error;
if (metadata.status !== 0) {
  throw new Error(`cargo metadata failed with exit code ${metadata.status}`);
}

console.log(`Applied app version ${version}${previousVersion === version ? ' (unchanged)' : ''}`);
