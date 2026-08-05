import { describe, expect, it } from 'bun:test';
import {
  parseCargoPackageVersion,
  setCargoLockPackageVersion,
  setCargoPackageVersion,
  validateAppVersion,
} from '../scripts/app-version.mjs';

describe('app version', () => {
  it('reads the version from the package section', () => {
    expect(parseCargoPackageVersion(`
[package]
name = "cpa-gui"
version = "1.2.3-beta.1+build.7"

[dependencies]
example = "9.9.9"
`)).toBe('1.2.3-beta.1+build.7');
  });

  it('rejects a missing package version', () => {
    expect(() => parseCargoPackageVersion('[package]\nname = "cpa-gui"\n'))
      .toThrow('Missing package version');
  });

  it('rejects an invalid semantic version', () => {
    expect(() => parseCargoPackageVersion('[package]\nversion = "v1.2.3"\n'))
      .toThrow('Invalid package version');
  });

  it('updates only the package version', () => {
    const manifest = `[package]
name = "cpa-gui"
version = "1.2.3"

[dependencies]
example = "9.9.9"
`;
    const updated = setCargoPackageVersion(manifest, '2.0.0-rc.1');
    expect(parseCargoPackageVersion(updated)).toBe('2.0.0-rc.1');
    expect(updated).toContain('example = "9.9.9"');
  });

  it('validates a version supplied by a release tag', () => {
    expect(validateAppVersion('0.2.15')).toBe('0.2.15');
    expect(() => validateAppVersion('v0.2.15')).toThrow('Invalid app version');
  });

  it('updates only the local app entry in Cargo.lock', () => {
    const lockfile = `[[package]]
name = "cpa-gui"
version = "1.2.3"

[[package]]
name = "dependency"
version = "1.2.3"
source = "registry+https://github.com/rust-lang/crates.io-index"
`;
    const updated = setCargoLockPackageVersion(lockfile, 'cpa-gui', '2.0.0');
    expect(updated).toContain('name = "cpa-gui"\nversion = "2.0.0"');
    expect(updated).toContain('name = "dependency"\nversion = "1.2.3"');
  });
});
