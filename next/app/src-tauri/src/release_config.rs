//! Validate final release inputs, not a source config before Tauri merges overrides.
use crate::trust::{read_bounded, TrustConfig, COMPILED_TEST_TRUST, PRODUCTION_TRUST};
use serde_json::Value;
use std::io::{Read, Seek, SeekFrom};
use std::{
    fs,
    path::{Path, PathBuf},
};

#[derive(Clone)]
pub struct ReleaseInputs {
    pub tauri_config: PathBuf,
    pub compiled_trust: PathBuf,
    pub packaged_trust: PathBuf,
    /// JSON array of all resolved cargo feature names for the app.
    pub features: PathBuf,
    /// Complete staged trust directory; only empty trust JSON is currently permitted.
    pub bundled_trust: PathBuf,
    pub artifacts: Vec<PathBuf>,
}
pub fn validate(inputs: &ReleaseInputs) -> Result<(), String> {
    if COMPILED_TEST_TRUST || cfg!(feature = "native-test") {
        return Err("release validator was compiled with a test feature".into());
    }
    empty_trust(PRODUCTION_TRUST.as_bytes())?;
    if inputs.artifacts.is_empty() {
        return Err("release validation requires every artifact output path".into());
    }
    for path in [
        &inputs.tauri_config,
        &inputs.compiled_trust,
        &inputs.packaged_trust,
        &inputs.features,
    ] {
        reject_test_path(path)?;
    }
    for path in &inputs.artifacts {
        reject_test_path(path)?;
    }
    let config = json_file(&inputs.tauri_config)?;
    let _: tauri::Config = serde_json::from_slice(&read_bounded(&inputs.tauri_config)?)
        .map_err(|_| "invalid final Tauri configuration")?;
    if config.get("identifier").and_then(Value::as_str) != Some(RELEASE_IDENTIFIER)
        || config.get("productName").and_then(Value::as_str) != Some("pluxy")
    {
        return Err("release identifier/product must be dev.pluxy.desktop/pluxy".into());
    }
    if let Some(updater) = config.pointer("/plugins/updater") {
        if updater != &Value::Null && updater != &serde_json::json!({}) {
            return Err("production updater trust not configured".into());
        }
    }
    if config
        .pointer("/bundle/createUpdaterArtifacts")
        .is_some_and(|v| v != &Value::Bool(false))
    {
        return Err("updater artifacts require production trust".into());
    }
    reject_test(&config)?;
    if let Some(name) = config.get("mainBinaryName").and_then(Value::as_str) {
        reject_test_path(Path::new(name))?;
    }
    // Trust resources declared by the final config must be checked even when omitted from the
    // separately staged trust directory. Reject unsupported globs rather than silently skipping.
    if let Some(resources) = config.pointer("/bundle/resources") {
        let names: Vec<&str> = if let Some(a) = resources.as_array() {
            a.iter()
                .map(|v| v.as_str().ok_or("resource path must be a string"))
                .collect::<Result<_, _>>()?
        } else if let Some(o) = resources.as_object() {
            o.keys().map(String::as_str).collect()
        } else {
            return Err("invalid bundle resources".into());
        };
        for name in names {
            if name.contains(['*', '?', '[']) {
                return Err("release trust validation requires expanded resource paths".into());
            }
            let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(name);
            scan_trust(&path)?;
        }
    }
    empty_trust(&read_bounded(&inputs.compiled_trust)?)?;
    empty_trust(&read_bounded(&inputs.packaged_trust)?)?;
    let features = json_file(&inputs.features)?;
    let features = features.as_array().ok_or("features must be a JSON array")?;
    for feature in features {
        let feature = feature.as_str().ok_or("feature must be a string")?;
        if feature.to_ascii_lowercase().contains("test")
            || feature == "local-build"
            || !feature
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"_-".contains(&b))
        {
            return Err("test cargo feature in release".into());
        }
    }
    scan_trust(&inputs.bundled_trust)?;
    // Compare with this validator's actual build. A stale OUT_DIR or source-only config cannot
    // describe a different app and still pass. Receipt presence binds at least the app executable.
    if config
        != serde_json::from_str::<Value>(include_str!(concat!(
            env!("OUT_DIR"),
            "/app-tauri-config.json"
        )))
        .unwrap()
    {
        return Err("Tauri config differs from compiled merged config".into());
    }
    if read_bounded(&inputs.compiled_trust)? != PRODUCTION_TRUST.as_bytes() {
        return Err("compiled trust snapshot mismatch".into());
    }
    if features
        != serde_json::from_str::<Value>(include_str!(concat!(
            env!("OUT_DIR"),
            "/app-cargo-features.json"
        )))
        .unwrap()
        .as_array()
        .unwrap()
    {
        return Err("cargo feature snapshot mismatch".into());
    }
    // The app executable's name in a package: Tauri's mainBinaryName, or the Cargo binary's name
    // (Cargo.toml [[bin]]) when the config sets none.
    let name = config
        .get("mainBinaryName")
        .and_then(Value::as_str)
        .unwrap_or("pluxy-app");
    for artifact in &inputs.artifacts {
        scan_artifact(artifact, name)?;
    }
    Ok(())
}
/// The release app's identifier (its macOS bundle identifier too).
const RELEASE_IDENTIFIER: &str = "dev.pluxy.desktop";

fn json_file(path: &Path) -> Result<Value, String> {
    serde_json::from_slice(&read_bounded(path)?)
        .map_err(|_| format!("{}: invalid JSON release input", path.display()))
}
fn empty_trust(bytes: &[u8]) -> Result<(), String> {
    let _: Value = serde_json::from_slice(bytes).map_err(|_| "invalid trust JSON")?;

    let config: TrustConfig = serde_json::from_slice(bytes)
        .map_err(|_| "invalid trust schema or private/unknown fields")?;
    if config.roots.iter().any(|r| r.kind != "production")
        || config
            .updater
            .as_ref()
            .is_some_and(|r| r.kind != "production")
    {
        return Err("test root in release trust".into());
    }
    if !config.roots.is_empty() || config.updater.is_some() {
        return Err(
            "production publisher/updater trust not configured; roots must be empty".into(),
        );
    }
    Ok(())
}
fn reject_test(v: &Value) -> Result<(), String> {
    match v {
        Value::String(s) => {
            let s = s.to_ascii_lowercase();
            if s.contains("private key")
                || s.contains("private_key")
                || s.contains("secret key")
                || s.contains("secret_key")
                || s.contains("pluxy test")
                || s.contains("dev.pluxy.desktop.test")
                || s.contains("test-trust")
                || s == "test"
            {
                return Err("test/private material in release config".into());
            }
        }
        Value::Array(a) => {
            for v in a {
                reject_test(v)?;
            }
        }
        Value::Object(o) => {
            for (key, value) in o {
                let k = key.to_ascii_lowercase().replace(['_', '-'], "");
                if k.contains("private")
                    || k.contains("secret")
                    || k.contains("test")
                    || k == "pubkey"
                    || k == "publickey"
                {
                    return Err("test/private/key input is not configured for release".into());
                }
                reject_test(value)?;
            }
        }
        _ => {}
    }
    Ok(())
}
fn reject_test_path(path: &Path) -> Result<(), String> {
    let name = path
        .file_name()
        .unwrap_or_default()
        .to_string_lossy()
        .to_ascii_lowercase();
    if name.contains("-test")
        || name.contains("pluxy test")
        || name.contains(".test")
        || name.contains("test-trust")
        || name.contains("private")
        || name.contains("secret")
    {
        return Err("test/private release input or artifact path".into());
    }
    Ok(())
}
fn scan_trust(path: &Path) -> Result<(), String> {
    reject_test_path(path)?;
    let meta = fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if meta.is_dir() {
        for entry in fs::read_dir(path).map_err(|e| e.to_string())? {
            scan_trust(&entry.map_err(|e| e.to_string())?.path())?;
        }
    } else if meta.is_file() && path.extension().is_some_and(|e| e == "json") {
        // A closed schema rejects PEM, binary seeds, extra fields and unknown bundled files.
        empty_trust(&read_bounded(path)?)?;
    } else {
        return Err("bundled trust may contain only regular empty trust JSON files".into());
    }
    Ok(())
}

/// Bytes no release artifact may hold, in a file given or in any file inside an AppImage given.
const FORBIDDEN: [(&[u8], &str); 4] = [
    (
        b"PLUXY_TEST_BUILD_DO_NOT_RELEASE",
        "test build marker/identity in artifact",
    ),
    (
        b"dev.pluxy.desktop.test",
        "test build marker/identity in artifact",
    ),
    (b"pluxy Test", "test build marker/identity in artifact"),
    // Tauri updater keys and signatures are base64 of minisign text that starts
    // "untrusted comment:"; production updater trust is not configured, so any one is a test key.
    (
        b"dW50cnVzdGVkIGNvbW1lbnQ6",
        "updater key or signature in artifact",
    ),
];

/// Checks one artifact; name is the app executable's name in a package. Each must be the app
/// executable (ELF, PE or Mach-O), a type-2 AppImage holding it at usr/bin/<name>, a Windows
/// folder bundle holding <name>.exe or a macOS app bundle holding Contents/MacOS/<name>, and
/// carry this build's receipt itself.
fn scan_artifact(path: &Path, name: &str) -> Result<(), String> {
    let meta =
        fs::symlink_metadata(path).map_err(|e| format!("artifact missing/unreadable: {e}"))?;
    let what = path.display().to_string();
    let receipt = if meta.is_dir() {
        scan_bundle(path, name, &what)?
    } else if meta.is_file() {
        let open = || fs::File::open(path).map_err(|e| format!("{what}: {e}"));
        match format(path)? {
            Format::Executable => scan_stream(open()?, &what)?,
            Format::AppImage(offset) => {
                // The files inside first, then every byte of the AppImage itself.
                let app = Path::new("usr/bin").join(name);
                let inside = scan_appimage(path, offset, &app)?;
                scan_stream(open()?, &what)?;
                inside
            }
        }
    } else {
        return Err("artifact must be a regular file or a bundle folder".into());
    };
    if !receipt {
        return Err(format!(
            "{what}: lacks the matching build receipt; include the actual app executable"
        ));
    }
    Ok(())
}

/// What an artifact is, from its first bytes; it is never run.
enum Format {
    /// An ELF, PE or Mach-O executable or library, such as the app's own binary.
    Executable,
    /// A type-2 AppImage, whose squashfs image starts at this offset.
    AppImage(u64),
}

/// The kinds of executable file the app is built as: Linux, Windows, macOS.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Exec {
    Elf,
    Pe,
    MachO,
}

/// How many first bytes of a file are read to tell what it is (an executable's headers lie in
/// them).
const HEAD: u64 = 512;

/// Reads an artifact's header. Anything that is not an executable (ELF, PE or Mach-O) or a type-2
/// AppImage cannot be looked inside, so it is refused. For an AppImage: the squashfs image must
/// start where the runtime's header says it ends, and nothing but zero padding may follow it.
fn format(path: &Path) -> Result<Format, String> {
    let what = path.display();
    let mut file = fs::File::open(path).map_err(|e| format!("{what}: {e}"))?;
    let mut header = Vec::new();
    file.by_ref()
        .take(HEAD)
        .read_to_end(&mut header)
        .map_err(|e| format!("{what}: {e}"))?;
    let unsupported = || {
        format!("{what}: not an app executable (ELF, PE or Mach-O) or a type-2 AppImage, so it cannot be checked")
    };
    let len = file.metadata().map_err(|e| format!("{what}: {e}"))?.len();
    if matches!(
        executable(&mut file, &header, len),
        Some(Exec::Pe | Exec::MachO)
    ) {
        return Ok(Format::Executable);
    }
    if header.len() < 20 || header[..4] != *b"\x7fELF" || !matches!(header[5], 1 | 2) {
        return Err(unsupported());
    }
    let field = |at: usize, len: usize| -> Result<u64, String> {
        let bytes = header
            .get(at..at + len)
            .ok_or_else(|| format!("{what}: short ELF header"))?;
        let mut value = 0u64;
        for (i, b) in bytes.iter().enumerate() {
            let shift = if header[5] == 1 { i } else { len - 1 - i };
            value |= u64::from(*b) << (8 * shift);
        }
        Ok(value)
    };
    // The AppImage specification's magic: "AI" and the type at offset 8.
    if header[8..10] != *b"AI" {
        return if well_formed_elf(&header, len) {
            Ok(Format::Executable)
        } else {
            Err(unsupported())
        };
    }
    if header[10] != 2 {
        return Err(format!(
            "{what}: AppImage type {} cannot be checked, only type 2",
            header[10]
        ));
    }
    // The runtime's own size: its section header table ends it (e_shoff + e_shentsize * e_shnum).
    let (shoff, shentsize, shnum) = match header[4] {
        1 => (field(0x20, 4)?, field(0x2e, 2)?, field(0x30, 2)?),
        2 => (field(0x28, 8)?, field(0x3a, 2)?, field(0x3c, 2)?),
        _ => return Err(format!("{what}: unknown ELF class")),
    };
    let offset = shentsize
        .checked_mul(shnum)
        .and_then(|n| n.checked_add(shoff))
        .ok_or_else(|| format!("{what}: ELF header out of range"))?;
    // The squashfs superblock (always little-endian): magic, inode count and bytes_used.
    let mut superblock = [0; 48];
    file.seek(SeekFrom::Start(offset))
        .and_then(|_| file.read_exact(&mut superblock))
        .map_err(|_| format!("{what}: no squashfs image where its runtime ends"))?;
    if superblock[..4] != *b"hsqs" {
        return Err(format!("{what}: no squashfs image where its runtime ends"));
    }
    let inodes = u32::from_le_bytes(superblock[4..8].try_into().unwrap());
    if u64::from(inodes) > UNPACKED_ENTRIES_MAX {
        return Err(format!("{what}: too many entries to unpack ({inodes})"));
    }
    let used = u64::from_le_bytes(superblock[40..48].try_into().unwrap());
    let end = offset
        .checked_add(used)
        .filter(|end| *end <= len)
        .ok_or_else(|| format!("{what}: its squashfs image is cut short"))?;
    let mut tail = Vec::new();
    file.seek(SeekFrom::Start(end))
        .and_then(|_| file.by_ref().take(4096).read_to_end(&mut tail))
        .map_err(|e| format!("{what}: {e}"))?;
    if len - end >= 4096 || tail.iter().any(|b| *b != 0) {
        return Err(format!(
            "{what}: data after its squashfs image (only zero padding may follow it)"
        ));
    }
    Ok(Format::AppImage(offset))
}

/// An ELF executable or shared object for a machine, with its own header size and program
/// headers that lie inside the file (len bytes): not just the first bytes of one. header is the
/// file's first bytes, at least its ELF header.
fn well_formed_elf(header: &[u8], len: u64) -> bool {
    if header.len() < 20 || header[..4] != *b"\x7fELF" || !matches!(header[5], 1 | 2) {
        return false;
    }
    let field = |at: usize, size: usize| -> Option<u64> {
        let bytes = header.get(at..at + size)?;
        let mut value = 0u64;
        for (i, b) in bytes.iter().enumerate() {
            let shift = if header[5] == 1 { i } else { size - 1 - i };
            value |= u64::from(*b) << (8 * shift);
        }
        Some(value)
    };
    let (ehsize, phentsize, phoff, sizes) = match header[4] {
        1 => (52, 32, field(0x1c, 4), 0x28),
        2 => (64, 56, field(0x20, 8), 0x34),
        _ => return false,
    };
    let check = || -> Option<bool> {
        let (phoff, phnum) = (phoff?, field(sizes + 4, 2)?);
        Some(
            matches!(field(0x10, 2)?, 2 | 3)
                && field(0x12, 2)? != 0
                && field(sizes, 2)? == ehsize
                && field(sizes + 2, 2)? == phentsize
                && phnum >= 1
                && phoff >= ehsize
                && phoff
                    .checked_add(phentsize * phnum)
                    .is_some_and(|end| end <= len),
        )
    };
    check() == Some(true)
}

/// What kind of executable a file is, from its first bytes (header), its length and, for a
/// universal Mach-O file, the headers of its parts read from file: an ELF file as
/// `well_formed_elf` takes one, a PE image, or a Mach-O file (thin or universal). Only headers are
/// read, never run; None for anything else, a bare magic number included.
fn executable(file: &mut (impl Read + Seek), header: &[u8], len: u64) -> Option<Exec> {
    if well_formed_elf(header, len) {
        Some(Exec::Elf)
    } else if well_formed_pe(header, len) {
        Some(Exec::Pe)
    } else if well_formed_macho(file, header, len) {
        Some(Exec::MachO)
    } else {
        None
    }
}

/// A PE image (Windows executable or DLL) for x86, x86-64 or arm64: the MZ header's e_lfanew
/// (past the MZ header's own 64 bytes) leads, inside header, to the PE signature and a file
/// header marked an executable image, whose optional header (PE32 or PE32+, at least its standard
/// and Windows-specific fields long) lies inside the file.
fn well_formed_pe(header: &[u8], len: u64) -> bool {
    let u16_at = |at: usize| -> Option<u16> {
        let b = header.get(at..at.checked_add(2)?)?;
        Some(u16::from_le_bytes([b[0], b[1]]))
    };
    let check = || -> Option<bool> {
        if !header.starts_with(b"MZ") {
            return Some(false);
        }
        let pe = u32::from_le_bytes(header.get(0x3c..0x40)?.try_into().ok()?) as usize;
        if pe < 0x40 || header.get(pe..pe.checked_add(4)?)? != b"PE\0\0" {
            return Some(false);
        }
        let machine = u16_at(pe.checked_add(4)?)?;
        let sections = u16_at(pe.checked_add(6)?)?;
        let optional = usize::from(u16_at(pe.checked_add(20)?)?);
        let characteristics = u16_at(pe.checked_add(22)?)?;
        let magic = u16_at(pe.checked_add(24)?)?;
        // The optional header's standard and Windows-specific fields: 96 bytes in PE32, 112 in
        // PE32+, before the data directories.
        let least = match magic {
            0x10b => 96,
            0x20b => 112,
            _ => return Some(false),
        };
        let end = pe.checked_add(24)?.checked_add(optional)?;
        Some(
            matches!(machine, 0x14c | 0x8664 | 0xaa64)
                && sections >= 1
                && characteristics & 0x0002 != 0
                && optional >= least
                && end as u64 <= len,
        )
    };
    check() == Some(true)
}

/// The Mach-O CPU types the app is built for: x86-64 and arm64.
const MACHO_CPUS: [u32; 2] = [0x0100_0007, 0x0100_000c];

/// A thin 64-bit Mach-O header (its first 32 bytes, h) for x86-64 or arm64: an executable,
/// library or bundle with at least one load command, each at least 8 bytes, all within room bytes
/// of where the header starts. Returns its CPU type.
fn thin_macho(h: &[u8], room: u64) -> Option<u32> {
    let le = |at: usize| -> Option<u32> {
        Some(u32::from_le_bytes(h.get(at..at + 4)?.try_into().ok()?))
    };
    let (magic, cpu, kind, commands, size) = (le(0)?, le(4)?, le(12)?, le(16)?, le(20)?);
    (magic == 0xfeed_facf
        && MACHO_CPUS.contains(&cpu)
        && matches!(kind, 2 | 6 | 8)
        && commands >= 1
        && u64::from(size) >= 8 * u64::from(commands)
        && 32 + u64::from(size) <= room)
        .then_some(cpu)
}

/// A Mach-O file for x86-64 or arm64: a thin 64-bit executable, library or bundle (`thin_macho`),
/// or a universal file of one to eight parts, each after the universal header, inside the file,
/// overlapping no other, and holding a thin header of the CPU type its entry names (read from
/// file). Every byte of the file, parts included, is scanned with the rest.
fn well_formed_macho(file: &mut (impl Read + Seek), header: &[u8], len: u64) -> bool {
    let be = |at: usize| -> Option<u32> {
        Some(u32::from_be_bytes(header.get(at..at + 4)?.try_into().ok()?))
    };
    let mut check = || -> Option<bool> {
        if header.get(..4)? == 0xfeed_facfu32.to_le_bytes() {
            return Some(thin_macho(header, len).is_some());
        }
        if be(0)? != 0xcafe_babe {
            return Some(false);
        }
        // A universal file; Java class files share its magic, and have a version past 8 there.
        let parts = be(4)? as usize;
        if !(1..=8).contains(&parts) {
            return Some(false);
        }
        let start = 8 + 20 * parts as u64;
        let mut slices = Vec::with_capacity(parts);
        for i in 0..parts {
            let at = 8 + 20 * i;
            let (cpu, offset, size) = (be(at)?, u64::from(be(at + 8)?), u64::from(be(at + 12)?));
            if offset < start || offset.checked_add(size)? > len {
                return Some(false);
            }
            slices.push((offset, size, cpu));
        }
        slices.sort_unstable();
        if slices.windows(2).any(|w| w[0].0 + w[0].1 > w[1].0) {
            return Some(false);
        }
        for (offset, size, cpu) in slices {
            let mut thin = [0u8; 32];
            file.seek(SeekFrom::Start(offset)).ok()?;
            file.read_exact(&mut thin).ok()?;
            if thin_macho(&thin, size) != Some(cpu) {
                return Some(false);
            }
        }
        Some(true)
    };
    check() == Some(true)
}

/// Checks a bundle folder given as an artifact: a macOS app bundle (a folder named `*.app`), whose
/// app executable is Contents/MacOS/<name>, a Mach-O file; or a Windows folder bundle, whose app
/// executable is <name>.exe at its top, a PE file. An app bundle's Info.plist must name the
/// release (`check_info_plist`). Every file in it is checked as a file inside an AppImage is.
/// Says whether the app executable carries the build's receipt.
fn scan_bundle(path: &Path, name: &str, what: &str) -> Result<bool, String> {
    let (app, kind) = if path.extension().is_some_and(|e| e == "app") {
        check_info_plist(path, name, what)?;
        (Path::new("Contents/MacOS").join(name), Exec::MachO)
    } else {
        // The names at the folder's top first, as an app bundle's Info.plist is read first: a
        // test program there refuses it for its name, whatever its folders hold and in whatever
        // order the system lists them.
        let mut top: Vec<String> = fs::read_dir(path)
            .map_err(|e| format!("{what}: {e}"))?
            .map(|e| e.map(|e| e.file_name().to_string_lossy().into_owned()))
            .collect::<Result<_, _>>()
            .map_err(|e| format!("{what}: {e}"))?;
        top.sort();
        for n in top {
            let lower = n.to_ascii_lowercase();
            if lower.contains("pluxy") && lower.contains("test") {
                return Err(format!("{what}!{n}: test file name in package"));
            }
        }
        (PathBuf::from(format!("{name}.exe")), Exec::Pe)
    };
    let mut budget = Budget::default();
    let mut receipt = false;
    scan_tree(path, path, (&app, kind), what, 0, &mut budget, &mut receipt)?;
    Ok(receipt)
}

/// An app bundle's Contents/Info.plist: a regular file of at most JSON_MAX bytes, a property list
/// (XML or binary) whose top is a dictionary naming the release identifier as CFBundleIdentifier
/// and name as CFBundleExecutable, with no test marker in any key, string or data in it. It is
/// decoded before it is checked, so an entity or another encoding cannot hide the test identity
/// from the byte scan.
fn check_info_plist(bundle: &Path, name: &str, what: &str) -> Result<(), String> {
    let path = bundle.join("Contents").join("Info.plist");
    let what = format!("{what}!Contents/Info.plist");
    let meta = fs::symlink_metadata(&path).map_err(|_| format!("{what}: missing"))?;
    if !meta.is_file() {
        return Err(format!("{what}: not a regular file"));
    }
    if meta.len() > JSON_MAX {
        return Err(format!("{what}: too large to check"));
    }
    let bytes = fs::read(&path).map_err(|e| format!("{what}: {e}"))?;
    let value = plist::Value::from_reader(std::io::Cursor::new(bytes))
        .map_err(|_| format!("{what}: not a property list"))?;
    fn marked(v: &plist::Value) -> Option<&'static str> {
        let hit = |b: &[u8]| {
            FORBIDDEN
                .iter()
                .find(|(m, _)| b.windows(m.len()).any(|w| w == *m))
                .map(|(_, why)| *why)
        };
        match v {
            plist::Value::String(s) => hit(s.as_bytes()),
            plist::Value::Data(d) => hit(d),
            plist::Value::Array(a) => a.iter().find_map(marked),
            plist::Value::Dictionary(d) => d
                .iter()
                .find_map(|(k, v)| hit(k.as_bytes()).or_else(|| marked(v))),
            _ => None,
        }
    }
    if let Some(why) = marked(&value) {
        return Err(format!("{what}: {why}"));
    }
    let dict = value
        .as_dictionary()
        .ok_or_else(|| format!("{what}: not a dictionary"))?;
    let string = |key: &str| dict.get(key).and_then(plist::Value::as_string);
    if string("CFBundleIdentifier") != Some(RELEASE_IDENTIFIER) {
        return Err(format!(
            "{what}: CFBundleIdentifier is not {RELEASE_IDENTIFIER}"
        ));
    }
    if string("CFBundleExecutable") != Some(name) {
        return Err(format!("{what}: CFBundleExecutable is not {name}"));
    }
    Ok(())
}

/// Streams reader with overlap, so markers split across reads are still found; no
/// artifact-sized allocation. Says whether the build's receipt is in it.
fn scan_stream(mut reader: impl Read, what: &str) -> Result<bool, String> {
    let receipt = env!("PLUXY_BUILD_RECEIPT").as_bytes();
    let forbidden: Vec<&[u8]> = FORBIDDEN.iter().map(|(m, _)| *m).collect();
    let overlap = FORBIDDEN
        .iter()
        .map(|(m, _)| m.len())
        .chain([receipt.len()])
        .max()
        .unwrap()
        - 1;
    let mut buffer = vec![0; 64 * 1024 + overlap];
    let mut kept = 0;
    let mut found = false;
    loop {
        let n = reader
            .read(&mut buffer[kept..])
            .map_err(|e| format!("{what}: {e}"))?;
        if n == 0 {
            break;
        }
        let window = &buffer[..kept + n];
        if let Some(i) = first_of(window, &forbidden) {
            return Err(format!("{what}: {}", FORBIDDEN[i].1));
        }
        found = found || first_of(window, &[receipt]).is_some();
        kept = overlap.min(window.len());
        let end = window.len();
        buffer.copy_within(end - kept..end, 0);
    }
    Ok(found)
}

/// The index of a needle found in hay. One pass, comparing only where a needle's first byte is:
/// the validator is a debug build, and an AppImage holds hundreds of megabytes.
fn first_of(hay: &[u8], needles: &[&[u8]]) -> Option<usize> {
    let mut starts = [false; 256];
    for needle in needles {
        starts[needle[0] as usize] = true;
    }
    for (i, b) in hay.iter().enumerate() {
        if starts[*b as usize] {
            if let Some(k) = needles.iter().position(|n| hay[i..].starts_with(n)) {
                return Some(k);
            }
        }
    }
    None
}

// Bounds for unpacking an AppImage: many times what the app's package needs.
const UNPACKED_BYTES_MAX: u64 = 2 << 30;
const UNPACKED_ENTRIES_MAX: u64 = 100_000;
const UNPACKED_DEPTH_MAX: usize = 32;
const UNPACK_SECONDS: u64 = 600;
/// JSON inside an AppImage larger than this is refused rather than parsed.
const JSON_MAX: u64 = 1 << 20;
/// unsquashfs's listing, and its other output and errors.
const LISTING_MAX: u64 = 64 << 20;
const OUTPUT_MAX: u64 = 1 << 20;

/// Unpacks an AppImage's squashfs image into a private temporary folder and checks every file in
/// it. Says whether its app executable (app, inside it) carries the build's receipt. The folder is
/// removed after, and a failure to remove it is an error.
fn scan_appimage(path: &Path, offset: u64, app: &Path) -> Result<bool, String> {
    let what = path.display().to_string();
    let mut random = [0; 12];
    getrandom::fill(&mut random).map_err(|e| e.to_string())?;
    let dir = std::env::temp_dir().join(format!(
        "pluxy-release-scan-{}",
        crate::artifact::hex(&random)
    ));
    #[cfg_attr(not(unix), allow(unused_mut))]
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
    builder
        .create(&dir)
        .map_err(|e| format!("{}: {e}", dir.display()))?;
    let result = unpack_and_scan(path, offset, app, &dir, &what);
    let removed = remove_unpacked(&dir)
        .map_err(|e| format!("{what}: could not remove {}: {e}", dir.display()));
    match (result, removed) {
        (Ok(receipt), Ok(())) => Ok(receipt),
        (Err(e), Ok(())) | (Ok(_), Err(e)) => Err(e),
        (Err(e), Err(r)) => Err(format!("{e}; {r}")),
    }
}

/// Removes the unpacked folder, making folders unpacked read-only writable first. Links are never
/// followed (file_type does not follow them).
fn remove_unpacked(dir: &Path) -> std::io::Result<()> {
    let mut folders = vec![dir.to_path_buf()];
    while let Some(folder) = folders.pop() {
        #[cfg(unix)]
        fs::set_permissions(&folder, std::os::unix::fs::PermissionsExt::from_mode(0o700))?;
        for entry in fs::read_dir(&folder)? {
            let entry = entry?;
            if entry.file_type()?.is_dir() {
                folders.push(entry.path());
            }
        }
    }
    fs::remove_dir_all(dir)
}

fn unpack_and_scan(
    path: &Path,
    offset: u64,
    app: &Path,
    dir: &Path,
    what: &str,
) -> Result<bool, String> {
    let offset = offset.to_string();
    let errors = dir.join("errors");
    // The listing first, before anything is unpacked: entry kinds, count, depth and size.
    let listing = dir.join("listing");
    run_bounded(
        std::process::Command::new("unsquashfs")
            .args(["-lls", "-offset", &offset])
            .arg(path),
        (&listing, LISTING_MAX),
        &errors,
        what,
    )?;
    let mut bytes = Vec::new();
    fs::File::open(&listing)
        .and_then(|f| f.take(LISTING_MAX + 1).read_to_end(&mut bytes))
        .map_err(|e| format!("{what}: {e}"))?;
    if bytes.len() as u64 > LISTING_MAX {
        return Err(format!(
            "{what}: unsquashfs listed more than {LISTING_MAX} bytes"
        ));
    }
    let listing = bytes;
    check_listing(&String::from_utf8_lossy(&listing), what)?;
    let root = dir.join("root");
    run_bounded(
        std::process::Command::new("unsquashfs")
            .args(["-no-progress", "-no-xattrs", "-offset", &offset, "-dest"])
            .arg(&root)
            .arg(path),
        (&dir.join("output"), OUTPUT_MAX),
        &errors,
        what,
    )?;
    let mut budget = Budget::default();
    let mut receipt = false;
    scan_tree(
        &root,
        &root,
        (app, Exec::Elf),
        what,
        0,
        &mut budget,
        &mut receipt,
    )?;
    Ok(receipt)
}

/// Runs unsquashfs with no input, for at most UNPACK_SECONDS, its output in files: out up to its
/// cap, errors up to OUTPUT_MAX. It is stopped when a file grows past its cap (checked as it runs,
/// so a file can overshoot by what it writes between checks).
fn run_bounded(
    command: &mut std::process::Command,
    (out, out_max): (&Path, u64),
    errors: &Path,
    what: &str,
) -> Result<(), String> {
    let over = || {
        [(out, out_max), (errors, OUTPUT_MAX)]
            .into_iter()
            .find(|(p, max)| fs::metadata(p).is_ok_and(|m| m.len() > *max))
            .map(|(p, max)| {
                format!(
                    "{what}: unsquashfs wrote more than {max} bytes to {}",
                    p.display()
                )
            })
    };
    let file = |p: &Path| fs::File::create(p).map_err(|e| format!("{}: {e}", p.display()));
    command
        .stdin(std::process::Stdio::null())
        .stdout(file(out)?)
        .stderr(file(errors)?);
    // On Windows std passes on every inheritable handle: not while one the host is starting a
    // process with is (`owned::windows`).
    #[cfg(windows)]
    let spawned = crate::owned::windows::std_spawn(command);
    #[cfg(not(windows))]
    let spawned = command.spawn();
    let mut child = spawned.map_err(|e| {
        format!("{what}: unsquashfs (package squashfs-tools) checks AppImages: {e}")
    })?;
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(UNPACK_SECONDS);
    let status = loop {
        if let Some(status) = child.try_wait().map_err(|e| format!("{what}: {e}"))? {
            break status;
        }
        if std::time::Instant::now() > deadline {
            let _ = child.kill();
            let _ = child.wait();
            return Err(format!("{what}: unsquashfs took over {UNPACK_SECONDS}s"));
        }
        if let Some(problem) = over() {
            let _ = child.kill();
            let _ = child.wait();
            return Err(problem);
        }
        std::thread::sleep(std::time::Duration::from_millis(20));
    };
    if let Some(problem) = over() {
        return Err(problem);
    }
    if !status.success() {
        let mut said = Vec::new();
        let _ = fs::File::open(errors).and_then(|f| f.take(4096).read_to_end(&mut said));
        return Err(format!(
            "{what}: unsquashfs could not unpack it: {}",
            String::from_utf8_lossy(&said).trim()
        ));
    }
    Ok(())
}

/// Checks `unsquashfs -lls` output before unpacking: only folders, regular files and links, and
/// the bounds on entries, depth and unpacked size. Lines that are not entries are skipped; the
/// unpacked tree is checked against the same bounds again.
fn check_listing(listing: &str, what: &str) -> Result<(), String> {
    let (mut entries, mut bytes) = (0u64, 0u64);
    for line in listing.lines() {
        let mut fields = line.split_whitespace();
        let mode = fields.next().unwrap_or_default();
        if mode.len() != 10 || !mode[1..].bytes().all(|b| b"rwxsStT-".contains(&b)) {
            continue;
        }
        entries += 1;
        match mode.as_bytes()[0] {
            b'd' | b'l' => {}
            b'-' => {
                let size = fields.nth(1).and_then(|s| s.parse::<u64>().ok());
                bytes = bytes.saturating_add(
                    size.ok_or_else(|| format!("{what}: unreadable listing line: {line}"))?,
                );
            }
            _ => return Err(format!("{what}: holds a device, pipe or socket: {line}")),
        }
        let path = line
            .find(" squashfs-root")
            .map(|i| &line[i + 1..])
            .ok_or_else(|| format!("{what}: unreadable listing line: {line}"))?;
        let path = path.split(" -> ").next().unwrap_or(path);
        if path.matches('/').count() > UNPACKED_DEPTH_MAX {
            return Err(format!("{what}: too deep to unpack: {path}"));
        }
    }
    if entries == 0 {
        return Err(format!("{what}: unsquashfs listed nothing"));
    }
    if entries > UNPACKED_ENTRIES_MAX {
        return Err(format!("{what}: too many entries to unpack ({entries})"));
    }
    if bytes > UNPACKED_BYTES_MAX {
        return Err(format!("{what}: too large to unpack ({bytes} bytes)"));
    }
    Ok(())
}

#[derive(Default)]
struct Budget {
    entries: u64,
    bytes: u64,
}

/// Checks a folder unpacked from an AppImage, or a bundle folder: names, links, contents, nested
/// containers and JSON. Sets receipt when its app executable (app's path, under root) is an
/// executable of app's kind with the build's receipt.
fn scan_tree(
    root: &Path,
    dir: &Path,
    app: (&Path, Exec),
    appimage: &str,
    depth: usize,
    budget: &mut Budget,
    receipt: &mut bool,
) -> Result<(), String> {
    if depth > UNPACKED_DEPTH_MAX {
        return Err(format!("{appimage}!{}: too deep", dir.display()));
    }
    for entry in fs::read_dir(dir).map_err(|e| format!("{appimage}: {e}"))? {
        let path = entry.map_err(|e| format!("{appimage}: {e}"))?.path();
        let what = format!(
            "{appimage}!{}",
            path.strip_prefix(root).unwrap_or(&path).display()
        );
        budget.entries += 1;
        if budget.entries > UNPACKED_ENTRIES_MAX {
            return Err(format!("{appimage}: too many entries to unpack"));
        }
        let name = path
            .file_name()
            .unwrap_or_default()
            .to_string_lossy()
            .to_ascii_lowercase();
        // Only pluxy's own test names: bundled system libraries may say "secret" or "-test".
        if name.contains("pluxy") && name.contains("test") {
            return Err(format!("{what}: test file name in package"));
        }
        let meta = fs::symlink_metadata(&path).map_err(|e| format!("{what}: {e}"))?;
        if meta.is_dir() {
            scan_tree(root, &path, app, appimage, depth + 1, budget, receipt)?;
        } else if meta.is_symlink() {
            let target = fs::read_link(&path).map_err(|e| format!("{what}: {e}"))?;
            let target = target.to_string_lossy().to_ascii_lowercase();
            if target.contains("pluxy") && target.contains("test") {
                return Err(format!("{what}: link to a test file name in package"));
            }
            check_link(root, &path, &what)?;
        } else if meta.is_file() {
            budget.bytes += meta.len();
            if budget.bytes > UNPACKED_BYTES_MAX {
                return Err(format!("{appimage}: too large to unpack"));
            }
            let mut file = fs::File::open(&path).map_err(|e| format!("{what}: {e}"))?;
            let mut head = Vec::new();
            file.by_ref()
                .take(HEAD)
                .read_to_end(&mut head)
                .map_err(|e| format!("{what}: {e}"))?;
            if let Some(kind) = container(&head) {
                return Err(format!(
                    "{what}: compressed or archive file ({kind}) cannot be checked"
                ));
            }
            // Past any amount of leading whitespace (the file is within the size budget).
            let first = first_byte(&head, &mut file).map_err(|e| format!("{what}: {e}"))?;
            file.seek(SeekFrom::Start(0))
                .map_err(|e| format!("{what}: {e}"))?;
            let has = scan_stream(&mut file, &what)?;
            if path.strip_prefix(root).is_ok_and(|p| p == app.0) {
                *receipt = has && executable(&mut file, &head, meta.len()) == Some(app.1);
            }
            check_json(&path, name.ends_with(".json"), first, meta.len(), &what)?;
        } else {
            return Err(format!("{what}: not a regular file, folder or link"));
        }
    }
    Ok(())
}

/// The first byte that is not ASCII whitespace: in head, else read from rest.
fn first_byte(head: &[u8], rest: &mut impl Read) -> std::io::Result<Option<u8>> {
    if let Some(b) = head.iter().find(|b| !b.is_ascii_whitespace()) {
        return Ok(Some(*b));
    }
    let mut buffer = vec![0; 64 * 1024];
    loop {
        let n = rest.read(&mut buffer)?;
        if n == 0 {
            return Ok(None);
        }
        if let Some(b) = buffer[..n].iter().find(|b| !b.is_ascii_whitespace()) {
            return Ok(Some(*b));
        }
    }
}

/// A link must lead, inside the package, to something that exists there. It is resolved as the
/// system would, one name at a time: a link met on the way is expanded in place (at most 40), and
/// a `..` may never leave the package's top folder. Nothing is followed out of the folder.
fn check_link(root: &Path, link: &Path, what: &str) -> Result<(), String> {
    use std::collections::VecDeque;
    use std::ffi::OsString;
    use std::path::Component;
    /// One step of a link's target.
    enum Part {
        Name(OsString),
        /// `..`
        Parent,
        /// A trailing `/` or `/.`, which components() drops: what comes before must be a folder.
        Folder,
    }
    /// A link's target, as steps to walk.
    fn parts(target: &Path, what: &str) -> Result<Vec<Part>, String> {
        let mut out = Vec::new();
        for part in target.components() {
            match part {
                Component::Normal(p) => out.push(Part::Name(p.to_os_string())),
                Component::CurDir => {}
                Component::ParentDir => out.push(Part::Parent),
                Component::RootDir | Component::Prefix(_) => {
                    return Err(format!("{what}: link to an absolute path"));
                }
            }
        }
        let text = target.to_string_lossy();
        if text.ends_with('/') || text.ends_with("/.") {
            out.push(Part::Folder);
        }
        Ok(out)
    }
    let mut here: Vec<OsString> = link
        .parent()
        .and_then(|p| p.strip_prefix(root).ok())
        .ok_or_else(|| format!("{what}: link outside the package"))?
        .iter()
        .map(|c| c.to_os_string())
        .collect();
    let read = |path: &Path| fs::read_link(path).map_err(|e| format!("{what}: {e}"));
    let mut todo: VecDeque<_> = parts(&read(link)?, what)?.into();
    let mut expanded = 1;
    while let Some(part) = todo.pop_front() {
        let name = match part {
            Part::Name(name) => name,
            Part::Parent => {
                if here.pop().is_none() {
                    return Err(format!("{what}: link leads outside the package"));
                }
                continue;
            }
            // The name before it was checked as a folder, since this followed it.
            Part::Folder => continue,
        };
        let next = here
            .iter()
            .chain([&name])
            .fold(root.to_path_buf(), |p, c| p.join(c));
        let meta =
            fs::symlink_metadata(&next).map_err(|_| format!("{what}: link to a missing file"))?;
        if meta.is_symlink() {
            expanded += 1;
            if expanded > 40 {
                return Err(format!("{what}: too many links in a row"));
            }
            for part in parts(&read(&next)?, what)?.into_iter().rev() {
                todo.push_front(part);
            }
        } else if !meta.is_dir() && !todo.is_empty() {
            // As the system requires: only a folder can have more of the path after it.
            return Err(format!("{what}: link goes through a file"));
        } else {
            here.push(name);
        }
    }
    Ok(())
}

/// The kind of a compressed or archive file, from its first bytes: none can be looked inside.
/// shortcut: only these common signatures are known; an encoding without one (raw deflate,
/// brotli, a custom format) is byte-scanned as it is, so test material inside it would pass. The
/// gate guards against our own build carrying test material, not a hostile builder; add a
/// signature here when a package format needs one.
fn container(head: &[u8]) -> Option<&'static str> {
    const MAGIC: [(&[u8], &str); 22] = [
        (b"\x1f\x8b", "gzip"),
        (b"BZh", "bzip2"),
        (b"\xfd7zXZ\x00", "xz"),
        (b"\x28\xb5\x2f\xfd", "zstd"),
        (b"\x04\x22\x4d\x18", "lz4"),
        (b"PK\x03\x04", "zip"),
        (b"PK\x05\x06", "zip"),
        (b"7z\xbc\xaf\x27\x1c", "7z"),
        (b"Rar!\x1a\x07", "rar"),
        (b"hsqs", "squashfs"),
        (b"070701", "cpio"),
        (b"070702", "cpio"),
        (b"070707", "cpio"),
        (b"!<arch>\n", "ar"),
        // zlib (RFC 1950) with its four usual compression-level flags.
        (b"\x78\x01", "zlib"),
        (b"\x78\x5e", "zlib"),
        (b"\x78\x9c", "zlib"),
        (b"\x78\xda", "zlib"),
        (b"\x5d\x00\x00", "lzma"),
        (b"LZIP", "lzip"),
        (b"\x89LZO\x00", "lzop"),
        (b"\x1f\x9d", "compress"),
    ];
    if let Some((_, kind)) = MAGIC.iter().find(|(m, _)| head.starts_with(m)) {
        return Some(kind);
    }
    if head.get(257..262) == Some(b"ustar") {
        return Some("tar");
    }
    if head.len() >= 11 && head[..4] == *b"\x7fELF" && head[8..10] == *b"AI" {
        return Some("AppImage");
    }
    None
}

/// JSON inside a package: a file named .json, or one whose first byte past whitespace is `{`. It is
/// decoded before it is checked, so escapes cannot hide a name or field; trust-shaped JSON,
/// whatever its name, must be empty trust; JSON that cannot be checked is refused, and so is any
/// unnamed file that starts like a JSON object and does not parse.
fn check_json(
    path: &Path,
    named: bool,
    first: Option<u8>,
    len: u64,
    what: &str,
) -> Result<(), String> {
    if !named && first != Some(b'{') {
        return Ok(());
    }
    if len > JSON_MAX {
        return Err(format!("{what}: JSON too large to check"));
    }
    let bytes = fs::read(path).map_err(|e| format!("{what}: {e}"))?;
    let value: Value = match serde_json::from_slice(&bytes) {
        Ok(value) => value,
        Err(_) if named => return Err(format!("{what}: invalid JSON")),
        // Escapes can hide a trust field from any byte search, so none is tried.
        Err(_) => {
            return Err(format!(
                "{what}: JSON that does not parse, so its trust fields cannot be checked"
            ))
        }
    };
    if value.get("roots").is_some() || value.get("updater").is_some() {
        empty_trust(&bytes).map_err(|e| format!("{what}: {e}"))?;
    }
    reject_test(&value).map_err(|e| format!("{what}: {e}"))
}
