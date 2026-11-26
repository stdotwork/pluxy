//! The parts of the macOS installer (`app.rs`) that are plain file work, built on macOS and, for
//! their tests, on Linux too: an app bundle's tree digest, copying a bundle, unpacking an update's
//! `.app.tar.gz` under strict rules, and what its `Info.plist` and program say.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]
use super::{sha256, MAX_BYTES, MAX_ENTRIES};
use std::fs;
use std::io::{Read, Write};
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Component, Path, PathBuf};

/// The `pluxy-tree-v1` digest of the bundle at root: the SHA-256 (hex) of `pluxy-tree-v1\n`
/// followed by a line per entry below root, sorted by relative path (`/`-separated, compared as
/// bytes): a folder `d <path>\n`, a regular file `f <x> <size> <SHA-256> <path>\n`, `<x>` being
/// `x` when any execute bit is set and `-` otherwise. A link or anything else has no digest.
pub(super) fn tree_digest(root: &Path) -> Result<String, String> {
    let m = fs::symlink_metadata(root).map_err(|e| format!("{}: {e}", root.display()))?;
    if !m.is_dir() {
        return Err(format!("{} is not a folder", root.display()));
    }
    let mut entries: Vec<(Vec<u8>, String)> = Vec::new();
    walk(root, root, &mut entries)?;
    entries.sort_by(|a, b| a.0.cmp(&b.0));
    let mut text = String::from("pluxy-tree-v1\n");
    for (_, line) in entries {
        text.push_str(&line);
    }
    Ok(sha256(text.as_bytes()))
}

fn walk(root: &Path, dir: &Path, out: &mut Vec<(Vec<u8>, String)>) -> Result<(), String> {
    for e in fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
        let e = e.map_err(|e| e.to_string())?;
        let path = e.path();
        let rel = relative(root, &path)?;
        let m = fs::symlink_metadata(&path).map_err(|e| format!("{}: {e}", path.display()))?;
        if m.is_dir() {
            out.push((rel.clone().into_bytes(), format!("d {rel}\n")));
            walk(root, &path, out)?;
        } else if m.is_file() {
            let mut f = fs::File::open(&path).map_err(|e| format!("{}: {e}", path.display()))?;
            let mut h = <sha2::Sha256 as sha2::Digest>::new();
            let mut buf = vec![0u8; 1 << 16];
            loop {
                let n = f.read(&mut buf).map_err(|e| e.to_string())?;
                if n == 0 {
                    break;
                }
                sha2::Digest::update(&mut h, &buf[..n]);
            }
            let x = if m.mode() & 0o111 != 0 { "x" } else { "-" };
            let digest = crate::artifact::hex(&sha2::Digest::finalize(h));
            out.push((
                rel.clone().into_bytes(),
                format!("f {x} {} {digest} {rel}\n", m.len()),
            ));
        } else {
            return Err(format!(
                "{} is a link or another kind of file, which an app bundle here may not hold",
                path.display()
            ));
        }
    }
    Ok(())
}

/// path's part below root, `/`-separated; refused unless it is UTF-8.
fn relative(root: &Path, path: &Path) -> Result<String, String> {
    let rel = path.strip_prefix(root).map_err(|e| e.to_string())?;
    let parts: Option<Vec<&str>> = rel.components().map(|c| c.as_os_str().to_str()).collect();
    parts
        .map(|p| p.join("/"))
        .ok_or_else(|| format!("{} has a name that is not UTF-8", path.display()))
}

/// A new folder at path, for this user only.
pub(super) fn private_new_dir(path: &Path) -> Result<(), String> {
    fs::DirBuilder::new()
        .mode(0o700)
        .create(path)
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// Copies the bundle from (folders and regular files only) to to, which must not exist: folders
/// and files keep their modes but for anyone else's write bits.
pub(super) fn copy_tree(from: &Path, to: &Path) -> Result<(), String> {
    let m = fs::symlink_metadata(from).map_err(|e| format!("{}: {e}", from.display()))?;
    if !m.is_dir() {
        return Err(format!("{} is not a folder", from.display()));
    }
    private_new_dir(to)?;
    for e in fs::read_dir(from).map_err(|e| format!("{}: {e}", from.display()))? {
        let e = e.map_err(|e| e.to_string())?;
        let (src, dst) = (e.path(), to.join(e.file_name()));
        let m = fs::symlink_metadata(&src).map_err(|e| format!("{}: {e}", src.display()))?;
        if m.is_dir() {
            copy_tree(&src, &dst)?;
        } else if m.is_file() {
            let mut r = fs::File::open(&src).map_err(|e| format!("{}: {e}", src.display()))?;
            let mut w = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&dst)
                .map_err(|e| format!("{}: {e}", dst.display()))?;
            std::io::copy(&mut r, &mut w).map_err(|e| format!("{}: {e}", dst.display()))?;
            w.set_permissions(fs::Permissions::from_mode((m.mode() & 0o755) | 0o600))
                .map_err(|e| e.to_string())?;
        } else {
            return Err(format!(
                "{} is a link or another kind of file, which an app bundle here may not hold",
                src.display()
            ));
        }
    }
    fs::set_permissions(to, fs::Permissions::from_mode((m.mode() & 0o755) | 0o700))
        .map_err(|e| e.to_string())
}

/// Unpacks the `.app.tar.gz` gz into the folder into, refusing (with into left to its caller to
/// remove) anything but: one top-level `<name>.app` folder; folders and regular files only (no
/// links, devices or other kinds), each once; relative paths without `..`; no setuid, setgid or
/// sticky bit; at most `MAX_ENTRIES` entries and `MAX_BYTES` bytes. Returns the bundle's path.
pub(super) fn unpack(gz: &[u8], into: &Path) -> Result<PathBuf, String> {
    let refuse = |what: String| Err(format!("The update's archive is refused: {what}."));
    let mut archive = tar::Archive::new(flate2::read::GzDecoder::new(gz));
    let entries = archive
        .entries()
        .map_err(|e| format!("The update is not a .tar.gz archive: {e}"))?;
    let (mut top, mut count, mut total) = (None::<String>, 0usize, 0u64);
    for entry in entries {
        let mut entry = entry.map_err(|e| format!("The update's archive cannot be read: {e}"))?;
        count += 1;
        if count > MAX_ENTRIES {
            return refuse(format!("it holds more than {MAX_ENTRIES} entries"));
        }
        let path = entry
            .path()
            .map_err(|e| format!("The update's archive cannot be read: {e}"))?
            .into_owned();
        let mut parts: Vec<String> = Vec::new();
        for c in path.components() {
            match c {
                Component::Normal(s) => match s.to_str() {
                    Some(s) => parts.push(s.to_string()),
                    None => return refuse(format!("{} is not a UTF-8 name", path.display())),
                },
                Component::CurDir => {}
                _ => return refuse(format!("{} is not a plain relative path", path.display())),
            }
        }
        let header = entry.header();
        let kind = header.entry_type();
        if parts.is_empty() && kind.is_dir() {
            continue;
        }
        let first = parts.first().cloned().unwrap_or_default();
        if !first.ends_with(".app") || first.starts_with('.') {
            return refuse(format!("{} is not inside one app bundle", path.display()));
        }
        match &top {
            None => top = Some(first),
            Some(t) if *t == first => {}
            Some(t) => return refuse(format!("it holds both {t} and {first}")),
        }
        let mode = header
            .mode()
            .map_err(|e| format!("The update's archive cannot be read: {e}"))?;
        if mode & 0o7000 != 0 {
            return refuse(format!(
                "{} has a setuid, setgid or sticky bit",
                path.display()
            ));
        }
        let dest = into.join(parts.join("/"));
        if kind.is_dir() {
            match fs::symlink_metadata(&dest) {
                Ok(m) if m.is_dir() => {}
                Ok(_) => return refuse(format!("{} is there twice", path.display())),
                Err(_) => fs::DirBuilder::new()
                    .recursive(true)
                    .mode(0o700)
                    .create(&dest)
                    .map_err(|e| format!("{}: {e}", dest.display()))?,
            }
        } else if kind.is_file() {
            let size = header
                .size()
                .map_err(|e| format!("The update's archive cannot be read: {e}"))?;
            total = total.saturating_add(size);
            if total > MAX_BYTES {
                return refuse(format!("it unpacks to more than {MAX_BYTES} bytes"));
            }
            if let Some(parent) = dest.parent() {
                fs::DirBuilder::new()
                    .recursive(true)
                    .mode(0o700)
                    .create(parent)
                    .map_err(|e| format!("{}: {e}", parent.display()))?;
            }
            let mut w = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&dest)
                .map_err(|e| {
                    format!(
                        "The update's archive is refused: {} ({e}; is it there twice?).",
                        path.display()
                    )
                })?;
            let n = std::io::copy(&mut (&mut entry).take(size), &mut w)
                .map_err(|e| format!("{}: {e}", dest.display()))?;
            if n != size {
                return refuse(format!("{} is cut short", path.display()));
            }
            w.flush().map_err(|e| e.to_string())?;
            w.set_permissions(fs::Permissions::from_mode((mode & 0o755) | 0o600))
                .map_err(|e| e.to_string())?;
        } else {
            return refuse(format!(
                "{} is a link, a device or another kind of entry, not a folder or a plain file",
                path.display()
            ));
        }
    }
    let top = top.ok_or("The update's archive is refused: it holds no app bundle.")?;
    // Folders were made for this user only while unpacking; now as anyone may read and enter
    // them, as a bundle's are.
    fn open_up(dir: &Path) -> Result<(), String> {
        fs::set_permissions(dir, fs::Permissions::from_mode(0o755)).map_err(|e| e.to_string())?;
        for e in fs::read_dir(dir).map_err(|e| e.to_string())? {
            let e = e.map_err(|e| e.to_string())?;
            if fs::symlink_metadata(e.path()).is_ok_and(|m| m.is_dir()) {
                open_up(&e.path())?;
            }
        }
        Ok(())
    }
    let bundle = into.join(top);
    open_up(&bundle)?;
    Ok(bundle)
}

/// What an app bundle's `Info.plist` says about it.
#[derive(Debug, PartialEq, Eq)]
pub(super) struct Info {
    pub(super) identifier: String,
    pub(super) executable: String,
    pub(super) version: String,
}

/// The bundle's `Contents/Info.plist`: its identifier, program name and version.
pub(super) fn info(bundle: &Path) -> Result<Info, String> {
    let path = bundle.join("Contents").join("Info.plist");
    let m = fs::symlink_metadata(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    if !m.is_file() {
        return Err(format!("{} is not a plain file", path.display()));
    }
    let v = plist::Value::from_file(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    let d = v
        .as_dictionary()
        .ok_or_else(|| format!("{} holds no dictionary", path.display()))?;
    let s = |k: &str| {
        d.get(k)
            .and_then(|v| v.as_string())
            .map(str::to_string)
            .ok_or_else(|| format!("{} has no {k}", path.display()))
    };
    Ok(Info {
        identifier: s("CFBundleIdentifier")?,
        executable: s("CFBundleExecutable")?,
        version: s("CFBundleShortVersionString")?,
    })
}

/// Whether head (a file's first bytes) starts a Mach-O program with code for cpu: a 64-bit thin
/// file of that CPU type, or a universal file with a part for it.
pub(super) fn macho_for(head: &[u8], cpu: u32) -> bool {
    let le = |i: usize| {
        head.get(i..i + 4)
            .map(|s| u32::from_le_bytes([s[0], s[1], s[2], s[3]]))
    };
    let be = |i: usize| {
        head.get(i..i + 4)
            .map(|s| u32::from_be_bytes([s[0], s[1], s[2], s[3]]))
    };
    match head.get(..4) {
        // MH_MAGIC_64, little-endian; filetype MH_EXECUTE.
        Some([0xcf, 0xfa, 0xed, 0xfe]) => le(4) == Some(cpu) && le(12) == Some(2),
        // FAT_MAGIC: big-endian fat_arch entries of 20 bytes after the count.
        Some([0xca, 0xfe, 0xba, 0xbe]) => {
            let Some(n) = be(4).filter(|n| (1..=16).contains(n)) else {
                return false;
            };
            (0..n as usize).any(|i| be(8 + 20 * i) == Some(cpu))
        }
        _ => false,
    }
}

/// Refused unless the bundle is a well-formed app bundle of want's identifier and program name
/// (and, with version, of that version) whose program has code for cpu.
pub(super) fn check(
    bundle: &Path,
    want: &Info,
    version: Option<&str>,
    cpu: u32,
) -> Result<(), String> {
    let got = info(bundle)?;
    if got.identifier != want.identifier || got.executable != want.executable {
        return Err(format!(
            "The update is the app bundle of {} ({}), not of this app, {} ({}).",
            got.identifier, got.executable, want.identifier, want.executable
        ));
    }
    if let Some(v) = version {
        if got.version != v {
            return Err(format!(
                "The update's bundle says it is version {}, not the announced {v}.",
                got.version
            ));
        }
    }
    let program = bundle.join("Contents").join("MacOS").join(&got.executable);
    let m = fs::symlink_metadata(&program).map_err(|e| format!("{}: {e}", program.display()))?;
    let mut head = vec![0u8; 4096];
    let n = fs::File::open(&program)
        .and_then(|mut f| f.read(&mut head))
        .map_err(|e| format!("{}: {e}", program.display()))?;
    if !m.is_file() || m.mode() & 0o100 == 0 || !macho_for(&head[..n], cpu) {
        return Err(format!(
            "{} is not a program for this Mac.",
            program.display()
        ));
    }
    Ok(())
}

/// The first link on path, path itself first and then each folder above it: the condition under
/// which Tauri refuses this process's own path on macOS (`StartingBinary`, which the updater
/// plugin needs to start), so a program started through a linked folder cannot update.
pub(super) fn linked(path: &Path) -> Option<&Path> {
    path.ancestors()
        .find(|a| fs::symlink_metadata(a).is_ok_and(|m| m.file_type().is_symlink()))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    pub(crate) const ARM64: u32 = 0x0100_000c;

    /// A minimal thin arm64 program header, then v.
    pub(crate) fn program(v: &str) -> Vec<u8> {
        let mut b = vec![0xcf, 0xfa, 0xed, 0xfe];
        b.extend_from_slice(&ARM64.to_le_bytes());
        b.extend_from_slice(&0u32.to_le_bytes());
        b.extend_from_slice(&2u32.to_le_bytes());
        b.extend_from_slice(&[0u8; 16]);
        b.extend_from_slice(v.as_bytes());
        b
    }

    pub(crate) fn plist(identifier: &str, executable: &str, version: &str) -> String {
        format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\
             <key>CFBundleIdentifier</key><string>{identifier}</string>\
             <key>CFBundleExecutable</key><string>{executable}</string>\
             <key>CFBundleShortVersionString</key><string>{version}</string>\
             </dict></plist>\n"
        )
    }

    /// A bundle `<dir>/pluxy Test.app` of version v whose program says contents.
    pub(crate) fn make(dir: &Path, v: &str, contents: &str) -> PathBuf {
        let bundle = dir.join("pluxy Test.app");
        let _ = fs::remove_dir_all(&bundle);
        fs::create_dir_all(bundle.join("Contents/MacOS")).unwrap();
        fs::create_dir_all(bundle.join("Contents/Resources")).unwrap();
        fs::write(
            bundle.join("Contents/Info.plist"),
            plist("dev.pluxy.desktop.test", "pluxy-app-test", v),
        )
        .unwrap();
        let exe = bundle.join("Contents/MacOS/pluxy-app-test");
        fs::write(&exe, program(contents)).unwrap();
        fs::set_permissions(&exe, fs::Permissions::from_mode(0o755)).unwrap();
        fs::write(bundle.join("Contents/Resources/icon.icns"), b"icon").unwrap();
        bundle
    }

    fn want() -> Info {
        Info {
            identifier: "dev.pluxy.desktop.test".into(),
            executable: "pluxy-app-test".into(),
            version: String::new(),
        }
    }

    fn temp(name: &str) -> PathBuf {
        super::super::tests::temp(&format!("bundle-{name}"))
    }

    /// An archive entry: path bytes, kind byte, mode, contents, link target.
    type Entry<'a> = (&'a [u8], u8, u32, &'a [u8], &'a str);

    /// A .tar.gz of entries.
    fn targz(entries: &[Entry]) -> Vec<u8> {
        let mut out = Vec::new();
        {
            let gz = flate2::write::GzEncoder::new(&mut out, flate2::Compression::fast());
            let mut b = tar::Builder::new(gz);
            for (path, kind, mode, data, link) in entries {
                let mut h = tar::Header::new_ustar();
                // Raw name bytes, so `..` and absolute paths can be written as an attacker would.
                h.as_old_mut().name[..path.len()].copy_from_slice(path);
                h.set_entry_type(tar::EntryType::new(*kind));
                h.set_mode(*mode);
                h.set_size(data.len() as u64);
                if !link.is_empty() {
                    h.set_link_name(link).unwrap();
                }
                h.set_cksum();
                b.append(&h, *data).unwrap();
            }
            b.into_inner().unwrap().finish().unwrap();
        }
        out
    }

    // The digest is the one the definition gives (the native test computes it the same way).
    #[test]
    fn the_tree_digest_is_its_definition() {
        let tmp = temp("digest");
        let bundle = make(&tmp, "0.1.0", "v1");
        let file = |rel: &str, x: &str| {
            let b = fs::read(bundle.join(rel)).unwrap();
            format!("f {x} {} {} {rel}\n", b.len(), sha256(&b))
        };
        let text = format!(
            "pluxy-tree-v1\nd Contents\n{}d Contents/MacOS\n{}d Contents/Resources\n{}",
            file("Contents/Info.plist", "-"),
            file("Contents/MacOS/pluxy-app-test", "x"),
            file("Contents/Resources/icon.icns", "-"),
        );
        assert_eq!(tree_digest(&bundle).unwrap(), sha256(text.as_bytes()));
        // Any change of contents, name or execute bit changes it; a link has none.
        let before = tree_digest(&bundle).unwrap();
        fs::set_permissions(
            bundle.join("Contents/Resources/icon.icns"),
            fs::Permissions::from_mode(0o755),
        )
        .unwrap();
        assert_ne!(tree_digest(&bundle).unwrap(), before);
        std::os::unix::fs::symlink("/etc/passwd", bundle.join("Contents/link")).unwrap();
        assert!(tree_digest(&bundle).is_err());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn a_copied_bundle_has_the_same_digest() {
        let tmp = temp("copy");
        let bundle = make(&tmp, "0.1.0", "v1");
        copy_tree(&bundle, &tmp.join("copy.app")).unwrap();
        assert_eq!(
            tree_digest(&bundle).unwrap(),
            tree_digest(&tmp.join("copy.app")).unwrap()
        );
        assert!(
            copy_tree(&bundle, &tmp.join("copy.app")).is_err(),
            "it must not exist"
        );
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn an_archive_of_one_bundle_unpacks_and_checks() {
        let tmp = temp("unpack");
        let plist = plist("dev.pluxy.desktop.test", "pluxy-app-test", "0.2.0");
        let prog = program("v2");
        let gz = targz(&[
            (b"pluxy Test.app/", b'5', 0o755, b"", ""),
            (b"pluxy Test.app/Contents/", b'5', 0o755, b"", ""),
            (
                b"pluxy Test.app/Contents/Info.plist",
                b'0',
                0o644,
                plist.as_bytes(),
                "",
            ),
            (
                b"pluxy Test.app/Contents/MacOS/pluxy-app-test",
                b'0',
                0o755,
                &prog,
                "",
            ),
        ]);
        let into = tmp.join("staging");
        private_new_dir(&into).unwrap();
        let bundle = unpack(&gz, &into).unwrap();
        assert_eq!(bundle, into.join("pluxy Test.app"));
        check(&bundle, &want(), Some("0.2.0"), ARM64).unwrap();
        let e = check(&bundle, &want(), Some("0.3.0"), ARM64).unwrap_err();
        assert!(e.contains("not the announced 0.3.0"), "{e}");
        assert!(
            check(&bundle, &want(), None, 0x0100_0007).is_err(),
            "x86_64"
        );
        let other = Info {
            identifier: "dev.pluxy.desktop".into(),
            ..want()
        };
        assert!(check(&bundle, &other, None, ARM64).is_err(), "identifier");
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn an_archive_breaking_a_rule_is_refused() {
        let ok: Entry = (b"x.app/Contents/Info.plist", b'0', 0o644, b"p", "");
        let cases: Vec<(&str, Vec<Entry>)> = vec![
            (
                "a link",
                vec![ok, (b"x.app/Contents/l", b'2', 0o777, b"", "/etc/passwd")],
            ),
            (
                "a hard link",
                vec![
                    ok,
                    (
                        b"x.app/Contents/h",
                        b'1',
                        0o644,
                        b"",
                        "x.app/Contents/Info.plist",
                    ),
                ],
            ),
            (
                "a device",
                vec![ok, (b"x.app/Contents/d", b'3', 0o644, b"", "")],
            ),
            (
                "a fifo",
                vec![ok, (b"x.app/Contents/f", b'6', 0o644, b"", "")],
            ),
            (
                "dot-dot",
                vec![(b"x.app/../../evil", b'0', 0o644, b"e", "")],
            ),
            (
                "absolute",
                vec![(b"/tmp/evil.app/x", b'0', 0o644, b"e", "")],
            ),
            (
                "setuid",
                vec![(b"x.app/Contents/MacOS/p", b'0', 0o4755, b"e", "")],
            ),
            (
                "two bundles",
                vec![ok, (b"y.app/Contents/Info.plist", b'0', 0o644, b"p", "")],
            ),
            (
                "not a bundle",
                vec![(b"Contents/Info.plist", b'0', 0o644, b"p", "")],
            ),
            ("twice", vec![ok, ok]),
            ("empty", vec![]),
        ];
        for (what, entries) in cases {
            let tmp = temp(&format!("refuse-{}", what.replace(' ', "-")));
            let into = tmp.join("staging");
            private_new_dir(&into).unwrap();
            let r = unpack(&targz(&entries), &into);
            assert!(r.is_err(), "{what}: {r:?}");
            assert!(!Path::new("/tmp/evil.app").exists(), "{what}");
            assert!(!tmp.join("evil").exists(), "{what}");
            fs::remove_dir_all(&tmp).unwrap();
        }
        let tmp = temp("refuse-gzip");
        assert!(unpack(b"not gzip", &tmp).is_err());
        fs::remove_dir_all(&tmp).unwrap();
    }

    #[test]
    fn only_a_program_for_the_cpu_counts() {
        assert!(macho_for(&program("x"), ARM64));
        assert!(!macho_for(&program("x"), 0x0100_0007));
        let mut fat = vec![0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 2];
        for cpu in [0x0100_0007u32, ARM64] {
            fat.extend_from_slice(&cpu.to_be_bytes());
            fat.extend_from_slice(&[0u8; 16]);
        }
        assert!(macho_for(&fat, ARM64) && macho_for(&fat, 0x0100_0007));
        assert!(!macho_for(&fat, 0x0000_0007));
        let mut library = program("x");
        library[12] = 6; // MH_DYLIB
        assert!(!macho_for(&library, ARM64));
        assert!(!macho_for(b"MZ\x90\x00", ARM64));
    }

    // A program started by a path with a link in it, at any level (the folder the bundle is in,
    // one above it, or the program itself), is found; a path without one is not.
    #[test]
    fn a_link_anywhere_on_the_programs_path_is_found() {
        // Resolved: on macOS the temporary folder is itself under /var, a link.
        let tmp = super::super::tests::temp("linked").canonicalize().unwrap();
        let real = tmp.join("real");
        let bundle = make(&real, "0.1.0", "x");
        let program = bundle.join("Contents/MacOS/pluxy-app-test");
        assert_eq!(linked(&program), None, "no link on {}", program.display());
        std::os::unix::fs::symlink(&real, tmp.join("via")).unwrap();
        let through = tmp
            .join("via")
            .join("pluxy Test.app/Contents/MacOS/pluxy-app-test");
        assert!(through.exists());
        assert_eq!(linked(&through), Some(tmp.join("via").as_path()));
        let above = tmp.join("above");
        std::os::unix::fs::symlink(&tmp, &above).unwrap();
        let higher = above.join("real/pluxy Test.app/Contents/MacOS/pluxy-app-test");
        assert_eq!(linked(&higher), Some(above.as_path()));
        let alias = bundle.join("Contents/MacOS/alias");
        std::os::unix::fs::symlink(&program, &alias).unwrap();
        assert_eq!(linked(&alias), Some(alias.as_path()));
        fs::remove_dir_all(&tmp).unwrap();
    }
}
