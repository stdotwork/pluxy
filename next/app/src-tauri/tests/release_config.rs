#![cfg(debug_assertions)]
use pluxy_app::release_config::{validate, ReleaseInputs};
use serde_json::{json, Value};
use std::{fs, path::PathBuf};

#[path = "../build_config.rs"]
mod build_config;

struct Fixture {
    dir: PathBuf,
    inputs: ReleaseInputs,
}
impl Fixture {
    fn new() -> Self {
        let mut random = [0; 12];
        getrandom::fill(&mut random).unwrap();
        // Parent directory names are deliberately private/test-like: only basenames are policy.
        let dir = std::env::temp_dir()
            .join(format!("ci-test-{}", pluxy_app::artifact::hex(&random)))
            .join(".private");
        fs::create_dir_all(&dir).unwrap();
        let inputs = ReleaseInputs {
            tauri_config: dir.join("app-tauri-config.json"),
            compiled_trust: dir.join("compiled.json"),
            packaged_trust: dir.join("packaged.json"),
            features: dir.join("features.json"),
            bundled_trust: dir.join("trust"),
            artifacts: vec![dir.join("pluxy-app")],
        };
        fs::create_dir(&inputs.bundled_trust).unwrap();
        let f = Self { dir, inputs };
        f.clean();
        f
    }
    fn clean(&self) {
        fs::write(
            &self.inputs.tauri_config,
            include_bytes!(concat!(env!("OUT_DIR"), "/app-tauri-config.json")),
        )
        .unwrap();
        fs::write(
            &self.inputs.compiled_trust,
            include_bytes!("../trust/production.json"),
        )
        .unwrap();
        fs::write(
            &self.inputs.packaged_trust,
            include_bytes!("../trust/production.json"),
        )
        .unwrap();
        fs::write(
            &self.inputs.features,
            include_bytes!(concat!(env!("OUT_DIR"), "/app-cargo-features.json")),
        )
        .unwrap();
        fs::write(
            &self.inputs.artifacts[0],
            elf(env!("PLUXY_BUILD_RECEIPT").as_bytes()),
        )
        .unwrap();
        for entry in fs::read_dir(&self.inputs.bundled_trust).unwrap() {
            fs::remove_file(entry.unwrap().path()).unwrap();
        }
    }
    fn config(&self, edit: impl FnOnce(&mut Value)) {
        let mut value =
            serde_json::from_slice(&fs::read(&self.inputs.tauri_config).unwrap()).unwrap();
        edit(&mut value);
        fs::write(
            &self.inputs.tauri_config,
            serde_json::to_vec(&value).unwrap(),
        )
        .unwrap();
    }
    fn refused(&self, reason: &str) {
        let error = validate(&self.inputs).unwrap_err();
        assert!(error.contains(reason), "expected {reason}, got {error}");
        self.clean();
        validate(&self.inputs).unwrap();
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        fs::remove_dir_all(self.dir.parent().unwrap()).unwrap();
    }
}

#[test]
fn release_config_clean_and_contamination_controls() {
    let f = Fixture::new();
    if cfg!(feature = "artifact-test-trust") || cfg!(feature = "native-test") {
        assert!(validate(&f.inputs)
            .unwrap_err()
            .contains("compiled with a test feature"));
        return;
    }
    validate(&f.inputs).unwrap();
    let mut seed = [0; 32];
    getrandom::fill(&mut seed).unwrap();
    let public = ed25519_dalek::SigningKey::from_bytes(&seed).verifying_key();
    let root = json!({"roots":[{"kind":"test","publisher":"example","source":"fixture","key_id":"runtime","public_key":pluxy_app::artifact::hex(public.as_bytes())}],"updater":null});
    for path in [&f.inputs.compiled_trust, &f.inputs.packaged_trust] {
        fs::write(path, serde_json::to_vec(&root).unwrap()).unwrap();
        f.refused("test root");
        fs::write(path, serde_json::to_vec(&json!({"roots":[],"updater":{"kind":"test","public_key":pluxy_app::artifact::hex(public.as_bytes())}})).unwrap()).unwrap();
        f.refused("test root");
        fs::write(
            path,
            serde_json::to_vec(
                &json!({"roots":[],"updater":null,"private_key":pluxy_app::artifact::hex(&seed)}),
            )
            .unwrap(),
        )
        .unwrap();
        f.refused("private/unknown fields");
    }
    f.config(|v| {
        v["plugins"] = json!({"updater":{"pubkey":pluxy_app::artifact::hex(public.as_bytes())}})
    });
    f.refused("production updater trust not configured");
    for feature in ["native-test", "artifact-test-trust"] {
        fs::write(
            &f.inputs.features,
            serde_json::to_vec(&vec![feature]).unwrap(),
        )
        .unwrap();
        f.refused("test cargo feature");
    }
    fs::write(&f.inputs.features, br#"["custom-protocol"]"#).unwrap();
    f.refused("feature snapshot mismatch");
    // Schema-valid bundle.resources references: the validator follows the actual referenced file.
    for (bytes, reason) in [
        (serde_json::to_vec(&root).unwrap(), "test root"),
        (
            serde_json::to_vec(
                &json!({"roots":[],"updater":null,"private_key":pluxy_app::artifact::hex(&seed)}),
            )
            .unwrap(),
            "private/unknown fields",
        ),
        (seed.to_vec(), "invalid trust JSON"),
    ] {
        fs::write(f.dir.join("resource.json"), bytes).unwrap();
        f.config(|v| v["bundle"]["resources"] = json!([f.dir.join("resource.json")]));
        f.refused(reason);
    }
    fs::write(
        f.inputs.bundled_trust.join("roots.json"),
        serde_json::to_vec(&root).unwrap(),
    )
    .unwrap();
    f.refused("test root");
    fs::write(
        f.inputs
            .bundled_trust
            .join(".pluxy-test-trust-profile.json"),
        b"{}",
    )
    .unwrap();
    f.refused("test/private");
    f.config(|v| v["identifier"] = json!("dev.pluxy.desktop.test"));
    f.refused("identifier/product");
    f.config(|v| v["productName"] = json!("pluxy Test"));
    f.refused("identifier/product");
    f.config(|v| v["version"] = json!("0.1.1"));
    f.refused("compiled merged config");
    fs::remove_file(&f.inputs.artifacts[0]).unwrap();
    f.refused("artifact missing");
    for marker in [
        "PLUXY_TEST_BUILD_DO_NOT_RELEASE",
        "dev.pluxy.desktop.test",
        "pluxy Test",
    ] {
        let mut bytes = vec![0; 65530];
        bytes.extend_from_slice(marker.as_bytes());
        fs::write(&f.inputs.artifacts[0], elf(&bytes)).unwrap();
        f.refused("test build marker/identity");
    }
    fs::write(&f.inputs.artifacts[0], elf(b"stale-build-receipt")).unwrap();
    f.refused("matching build receipt");
    let mut bad = f.inputs.clone();
    bad.artifacts = vec![f.dir.join("pluxy-test.AppImage")];
    fs::write(&bad.artifacts[0], env!("PLUXY_BUILD_RECEIPT")).unwrap();
    assert!(validate(&bad).unwrap_err().contains("test/private"));
    validate(&f.inputs).unwrap();
}

#[test]
fn merged_tauri_config_matches_build_order() {
    let f = Fixture::new();
    fs::write(f.dir.join("tauri.conf.json"), br#"{"identifier":"dev.pluxy.desktop","productName":"pluxy","plugins":{"updater":{"pubkey":"base"}}}"#).unwrap();
    fs::write(
        f.dir.join("tauri.linux.conf.json"),
        br#"{"plugins":{"updater":{"pubkey":"platform"}}}"#,
    )
    .unwrap();
    let config = build_config::merged(&f.dir, "x86_64-unknown-linux-gnu", None).unwrap();
    assert_eq!(
        config.pointer("/plugins/updater/pubkey").unwrap(),
        "platform"
    );
    let config = build_config::merged(
        &f.dir,
        "x86_64-unknown-linux-gnu",
        Some(r#"{"plugins":{"updater":{"pubkey":"environment"}}}"#),
    )
    .unwrap();
    assert_eq!(
        config.pointer("/plugins/updater/pubkey").unwrap(),
        "environment"
    );
    assert_eq!(config["identifier"], "dev.pluxy.desktop");
}

/// What goes into a fixture AppImage's image.
#[derive(Clone, Copy)]
enum Entry<'a> {
    File(&'a [u8]),
    Link(&'a str),
    /// A file of this many bytes that takes no space (all holes).
    Sparse(u64),
    /// A folder with mode 0555, set after everything inside it is written.
    Locked,
}

/// A minimal well-formed ELF64 little-endian shared object for x86-64: its 64-byte header, one
/// program header right after it, then body. What the validator takes for an app executable.
fn elf(body: &[u8]) -> Vec<u8> {
    let mut bytes = vec![0u8; 64 + 56];
    bytes[..4].copy_from_slice(b"\x7fELF");
    bytes[4] = 2; // ELF64
    bytes[5] = 1; // little-endian
    bytes[6] = 1;
    bytes[0x10] = 3; // e_type: shared object
    bytes[0x12] = 62; // e_machine: x86-64
    bytes[0x14] = 1; // e_version
    bytes[0x20] = 64; // e_phoff
    bytes[0x34] = 64; // e_ehsize
    bytes[0x36] = 56; // e_phentsize
    bytes[0x38] = 1; // e_phnum
    bytes.extend_from_slice(body);
    bytes
}

/// Just an ELF header's first 20 bytes (magic, ELF64, little-endian, e_type 3), then body: no
/// machine and no program headers, so not a real executable.
fn fabricated_elf(body: &[u8]) -> Vec<u8> {
    let mut bytes = vec![0u8; 20];
    bytes[..4].copy_from_slice(b"\x7fELF");
    bytes[4] = 2;
    bytes[5] = 1;
    bytes[6] = 1;
    bytes[0x10] = 3;
    bytes.extend_from_slice(body);
    bytes
}

/// bytes in a zlib stream (RFC 1950): the raw deflate data from gzip, between a zlib header and
/// its Adler-32.
fn zlib(bytes: &[u8]) -> Vec<u8> {
    let gz = gzip(bytes);
    let (mut a, mut b) = (1u32, 0u32);
    for byte in bytes {
        a = (a + u32::from(*byte)) % 65521;
        b = (b + a) % 65521;
    }
    let mut out = vec![0x78, 0x9c];
    out.extend_from_slice(&gz[10..gz.len() - 8]);
    out.extend_from_slice(&((b << 16) | a).to_be_bytes());
    out
}

/// bytes compressed by the gzip program.
fn gzip(bytes: &[u8]) -> Vec<u8> {
    use std::io::Write;
    let mut child = std::process::Command::new("gzip")
        .args(["-c", "-n"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    child.stdin.take().unwrap().write_all(bytes).unwrap();
    let out = child.wait_with_output().unwrap();
    assert!(out.status.success());
    out.stdout
}

/// A fixture AppImage at path: a 64-byte ELF64 header with the AppImage magic and type at offset
/// 8 and its section header table ending at runtime.len() (no sections), then the runtime bytes
/// after the header, then a squashfs image of files made by mksquashfs (squashfs-tools). It is
/// never run: the validator must find the image from the header alone.
fn appimage(path: &std::path::Path, kind: u8, runtime: &[u8], files: &[(&str, Entry)]) {
    use std::os::unix::fs::PermissionsExt;
    let dir = path.with_extension("AppDir");
    let mut locked = Vec::new();
    for (name, entry) in files {
        let file = dir.join(name);
        fs::create_dir_all(file.parent().unwrap()).unwrap();
        match entry {
            Entry::File(bytes) => fs::write(&file, bytes).unwrap(),
            Entry::Link(target) => std::os::unix::fs::symlink(target, &file).unwrap(),
            Entry::Sparse(len) => fs::File::create(&file).unwrap().set_len(*len).unwrap(),
            Entry::Locked => {
                fs::create_dir_all(&file).unwrap();
                locked.push(file);
            }
        }
    }
    for folder in &locked {
        fs::set_permissions(folder, fs::Permissions::from_mode(0o555)).unwrap();
    }
    let image = path.with_extension("squashfs");
    let made = std::process::Command::new("mksquashfs")
        .arg(&dir)
        .arg(&image)
        .args([
            "-noappend",
            "-no-progress",
            "-quiet",
            "-root-owned",
            "-comp",
            "gzip",
        ])
        .status()
        .expect("mksquashfs (package squashfs-tools) builds the fixture AppImages");
    assert!(made.success());
    let mut header = vec![0u8; 64];
    header[..4].copy_from_slice(b"\x7fELF");
    header[4] = 2; // ELF64
    header[5] = 1; // little-endian
    header[6] = 1;
    header[8..11].copy_from_slice(&[b'A', b'I', kind]);
    let end = (64 + runtime.len()) as u64;
    header[0x28..0x30].copy_from_slice(&end.to_le_bytes()); // e_shoff; e_shnum stays 0
    let mut bytes = header;
    bytes.extend_from_slice(runtime);
    bytes.extend_from_slice(&fs::read(&image).unwrap());
    fs::write(path, bytes).unwrap();
    for folder in &locked {
        fs::set_permissions(folder, fs::Permissions::from_mode(0o755)).unwrap();
    }
    fs::remove_dir_all(&dir).unwrap();
    fs::remove_file(&image).unwrap();
}

/// Records a case whose result is not the expected one, so that one run shows every case.
fn expect(wrong: &mut Vec<String>, case: &str, result: Result<(), String>, reason: Option<&str>) {
    match (result, reason) {
        (Ok(()), None) => {}
        (Err(error), Some(reason)) if error.contains(reason) => {}
        (got, want) => wrong.push(format!("{case}: expected {want:?}, got {got:?}")),
    }
}

/// Fixture AppImages built around a clean set of files that passes.
struct Images {
    f: Fixture,
    image: PathBuf,
    /// An app executable carrying this build's receipt.
    app: Vec<u8>,
}
impl Images {
    /// None when the validator was built with a test feature: it then refuses before any
    /// artifact (release_config_clean_and_contamination_controls).
    fn new() -> Option<Self> {
        let f = Fixture::new();
        if cfg!(feature = "artifact-test-trust") || cfg!(feature = "native-test") {
            return None;
        }
        Some(Self {
            image: f.dir.join("pluxy.AppImage"),
            app: elf(env!("PLUXY_BUILD_RECEIPT").as_bytes()),
            f,
        })
    }
    fn clean(&self) -> Vec<(&str, Entry<'_>)> {
        vec![
            ("AppRun", Entry::File(b"#!/bin/sh\n")),
            ("usr/bin/pluxy-app", Entry::File(&self.app)),
            // Bundled system libraries may have names a release input may not.
            ("usr/lib/libsecret-1.so.0", Entry::File(b"library")),
            ("usr/lib/libsecret-1.so", Entry::Link("libsecret-1.so.0")),
            (
                "usr/share/trust/app-package-trust.json",
                Entry::File(br#"{"roots":[],"updater":null}"#),
            ),
            (
                "usr/share/metainfo.json",
                Entry::File(br#"{"name":"pluxy"}"#),
            ),
            (
                "usr/share/applications/pluxy.desktop",
                Entry::File(b"[Desktop Entry]\nName=pluxy\n"),
            ),
            (
                "pluxy.desktop",
                Entry::Link("usr/share/applications/pluxy.desktop"),
            ),
        ]
    }
    /// Builds the AppImage from the clean files and extra, and validates it alone.
    fn check(&self, extra: &[(&str, Entry)], runtime: &[u8], kind: u8) -> Result<(), String> {
        let mut files = self.clean();
        files.extend_from_slice(extra);
        appimage(&self.image, kind, runtime, &files);
        self.validate(&[&self.image])
    }
    fn validate(&self, artifacts: &[&std::path::Path]) -> Result<(), String> {
        let mut inputs = self.f.inputs.clone();
        inputs.artifacts = artifacts.iter().map(|p| p.to_path_buf()).collect();
        validate(&inputs)
    }
}

// An AppImage is unpacked and every file in it checked; each refusal has the clean AppImage on
// the same route as its positive control.
#[test]
fn release_config_checks_inside_appimages() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let mut case = |name: &str, extra: &[(&str, Entry)], runtime: &[u8], kind: u8, reason| {
        let result = t.check(extra, runtime, kind);
        expect(&mut wrong, name, result, reason);
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    };
    let marker = b"..PLUXY_TEST_BUILD_DO_NOT_RELEASE..";
    let root = serde_json::to_vec(&json!({"roots":[{"kind":"test","publisher":"example","source":"fixture","key_id":"runtime","public_key":"00".repeat(32)}],"updater":null})).unwrap();
    case(
        "a marker in a file",
        &[("usr/lib/libmarked.so", Entry::File(marker))],
        b"runtime",
        2,
        Some("pluxy.AppImage!usr/lib/libmarked.so: test build marker/identity"),
    );
    case(
        "the test product name",
        &[(
            "usr/share/applications/pluxy.desktop",
            Entry::File(b"Name=pluxy Test\n"),
        )],
        b"runtime",
        2,
        Some("test build marker/identity"),
    );
    case(
        "an updater key",
        &[(
            "usr/share/key.pub",
            Entry::File(b"dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWdu"),
        )],
        b"runtime",
        2,
        Some("updater key or signature"),
    );
    case(
        "a test file name",
        &[("usr/bin/pluxy-app-test", Entry::File(b"x"))],
        b"runtime",
        2,
        Some("test file name in package"),
    );
    case(
        "a link to a test name",
        &[("icon", Entry::Link("pluxy Test.png"))],
        b"runtime",
        2,
        Some("link to a test file name"),
    );
    case(
        "trust JSON with a test root",
        &[("usr/share/roots.json", Entry::File(&root))],
        b"runtime",
        2,
        Some("test root"),
    );
    // The runtime part is checked as bytes too.
    case(
        "a marker in the runtime",
        &[],
        marker,
        2,
        Some("pluxy.AppImage: test build marker/identity"),
    );
    case(
        "type 1",
        &[],
        b"runtime",
        1,
        Some("AppImage type 1 cannot be checked"),
    );
    // A header that does not end where the image starts is refused, not guessed.
    t.check(&[], b"runtime", 2).unwrap();
    let mut bytes = fs::read(&t.image).unwrap();
    bytes[0x28] += 1;
    fs::write(&t.image, bytes).unwrap();
    expect(
        &mut wrong,
        "a wrong header",
        t.validate(&[&t.image]),
        Some("no squashfs image where its runtime ends"),
    );
    // Without the receipt anywhere inside, the AppImage does not describe this build.
    appimage(
        &t.image,
        2,
        b"runtime",
        &[("AppRun", Entry::File(b"#!/bin/sh\n"))],
    );
    expect(
        &mut wrong,
        "no receipt",
        t.validate(&[&t.image]),
        Some("matching build receipt"),
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 2: an artifact must be the app executable or a type-2 AppImage; anything else
// cannot be looked inside, so it is refused rather than byte-scanned.
#[test]
fn release_config_refuses_unsupported_artifact_formats() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let exe = t.f.dir.join("pluxy-app");
    fs::write(&exe, &t.app).unwrap();
    expect(&mut wrong, "the app executable", t.validate(&[&exe]), None);
    let gz = t.f.dir.join("pluxy.AppImage");
    fs::write(&gz, gzip(&elf(b"PLUXY_TEST_BUILD_DO_NOT_RELEASE"))).unwrap();
    expect(
        &mut wrong,
        "a gzip file holding a test executable, beside the app executable",
        t.validate(&[&exe, &gz]),
        Some("cannot be checked"),
    );
    let text = t.f.dir.join("notes.txt");
    fs::write(&text, env!("PLUXY_BUILD_RECEIPT")).unwrap();
    expect(
        &mut wrong,
        "a text file with the receipt",
        t.validate(&[&text]),
        Some("cannot be checked"),
    );
    expect(
        &mut wrong,
        "the app executable again",
        t.validate(&[&exe]),
        None,
    );
    // Round 2: an ELF header alone is not an executable.
    let fabricated = t.f.dir.join("pluxy-app-fabricated");
    fs::write(
        &fabricated,
        fabricated_elf(env!("PLUXY_BUILD_RECEIPT").as_bytes()),
    )
    .unwrap();
    expect(
        &mut wrong,
        "a fabricated ELF header with the receipt",
        t.validate(&[&fabricated]),
        Some("cannot be checked"),
    );
    expect(
        &mut wrong,
        "the app executable once more",
        t.validate(&[&exe]),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 3: a compressed or archive file inside an AppImage cannot be looked inside.
#[test]
fn release_config_refuses_containers_inside_appimages() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let payload = gzip(&elf(b"PLUXY_TEST_BUILD_DO_NOT_RELEASE"));
    let mut nested = vec![0u8; 64];
    nested[..4].copy_from_slice(b"\x7fELF");
    nested[8..11].copy_from_slice(b"AI\x02");
    for (name, file) in [
        ("usr/share/payload.gz", &payload[..]),
        ("usr/share/inner.AppImage", &nested[..]),
        ("usr/share/archive", b"PK\x03\x04archive"),
    ] {
        expect(
            &mut wrong,
            name,
            t.check(&[(name, Entry::File(file))], b"runtime", 2),
            Some("compressed or archive file"),
        );
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    }
    // Round 2: zlib streams are refused too.
    let deflated = zlib(&elf(b"PLUXY_TEST_BUILD_DO_NOT_RELEASE"));
    expect(
        &mut wrong,
        "a zlib stream",
        t.check(
            &[("usr/share/payload.z", Entry::File(&deflated))],
            b"runtime",
            2,
        ),
        Some("compressed or archive file"),
    );
    expect(
        &mut wrong,
        "the clean AppImage",
        t.check(&[], b"runtime", 2),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 4: JSON inside an AppImage is decoded before it is checked, trust-shaped JSON
// must be empty trust whatever its name, and JSON that cannot be checked is refused.
#[test]
fn release_config_checks_json_inside_appimages() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let root = serde_json::to_vec(&json!({"roots":[{"kind":"test","publisher":"example","source":"fixture","key_id":"runtime","public_key":"00".repeat(32)}],"updater":null})).unwrap();
    let mut padded = root.clone();
    padded.extend(vec![b' '; 70 * 1024]);
    let mut huge = vec![b' '; 2 << 20];
    huge.extend_from_slice(b"{}");
    for (name, file, reason) in [
        (
            "usr/share/a.json",
            &br#"{"name":"pluxy \u0054est"}"#[..],
            "test/private",
        ),
        (
            "usr/share/b.json",
            &br#"{"\u0070rivate_key":"x"}"#[..],
            "test/private",
        ),
        ("usr/share/roots.dat", &root[..], "test root"),
        (
            "usr/share/roots.json",
            &br#"{"roots":[],"updater":null,"extra":1}"#[..],
            "invalid trust schema",
        ),
        ("usr/share/padded.json", &padded[..], "test root"),
        ("usr/share/huge.json", &huge[..], "too large"),
        ("usr/share/bad.json", &b"{not json"[..], "invalid JSON"),
    ] {
        expect(
            &mut wrong,
            name,
            t.check(&[(name, Entry::File(file))], b"runtime", 2),
            Some(reason),
        );
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    }
    // Round 2: leading whitespace past the first 512 bytes does not hide JSON, and JSON that does
    // not parse but names trust fields is refused.
    let mut spaced = vec![b' '; 600];
    spaced.extend_from_slice(&root);
    let truncated = &root[..root.len() - 3];
    for (name, file, reason) in [
        ("usr/share/spaced.dat", &spaced[..], "test root"),
        ("usr/share/truncated.dat", truncated, "trust"),
    ] {
        expect(
            &mut wrong,
            name,
            t.check(&[(name, Entry::File(file))], b"runtime", 2),
            Some(reason),
        );
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    }
    expect(
        &mut wrong,
        "an unnamed file that starts like JSON with no trust fields",
        t.check(
            &[("usr/share/notes.dat", Entry::File(b"{ not json"))],
            b"runtime",
            2,
        ),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 5: nothing may follow the squashfs image but its zero padding.
#[test]
fn release_config_refuses_data_after_the_squashfs_image() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    for (name, tail) in [
        ("a marker", &b"PLUXY_TEST_BUILD_DO_NOT_RELEASE"[..]),
        ("zeros past the padding", &[0u8; 5000][..]),
    ] {
        t.check(&[], b"runtime", 2).unwrap();
        let mut bytes = fs::read(&t.image).unwrap();
        bytes.extend_from_slice(tail);
        fs::write(&t.image, bytes).unwrap();
        expect(
            &mut wrong,
            name,
            t.validate(&[&t.image]),
            Some("after its squashfs image"),
        );
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    }
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 6: a link inside an AppImage must stay inside it and lead to a file there.
#[test]
fn release_config_refuses_escaping_or_missing_links() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    for (name, target, reason) in [
        ("usr/lib/host", "/etc/hostname", "absolute"),
        ("usr/lib/up", "../../../outside", "outside the package"),
        ("usr/lib/missing", "nothing-here", "missing"),
    ] {
        expect(
            &mut wrong,
            name,
            t.check(&[(name, Entry::Link(target))], b"runtime", 2),
            Some(reason),
        );
        expect(
            &mut wrong,
            "the clean AppImage",
            t.check(&[], b"runtime", 2),
            None,
        );
    }
    expect(
        &mut wrong,
        "a link to a link inside",
        t.check(
            &[("usr/lib/libsecret.so", Entry::Link("libsecret-1.so"))],
            b"runtime",
            2,
        ),
        None,
    );
    // Round 2: links inside a target are followed as the system would, so a link to "." and a
    // ".." after it lead out of the package even though the text alone would stay inside.
    expect(
        &mut wrong,
        "a/../errors through a link to .",
        t.check(
            &[
                ("a", Entry::Link(".")),
                ("errors", Entry::File(b"x")),
                ("leak", Entry::Link("a/../errors")),
            ],
            b"runtime",
            2,
        ),
        Some("outside the package"),
    );
    expect(
        &mut wrong,
        "the clean AppImage",
        t.check(&[], b"runtime", 2),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 7: unpacking is bounded: depth, entries and unpacked size are checked, the
// size before anything is unpacked.
#[test]
fn release_config_bounds_unpacking() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let deep = format!("{}file", "d/".repeat(70));
    expect(
        &mut wrong,
        "70 folders deep",
        t.check(&[(&deep, Entry::File(b"x"))], b"runtime", 2),
        Some("too deep"),
    );
    expect(
        &mut wrong,
        "the clean AppImage",
        t.check(&[], b"runtime", 2),
        None,
    );
    expect(
        &mut wrong,
        "a 3 GiB sparse file",
        t.check(&[("usr/share/big", Entry::Sparse(3 << 30))], b"runtime", 2),
        Some("too large to unpack"),
    );
    expect(
        &mut wrong,
        "the clean AppImage",
        t.check(&[], b"runtime", 2),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 8: every artifact needs this build's receipt itself, an AppImage in an app
// executable inside it.
#[test]
fn release_config_needs_the_receipt_in_every_artifact() {
    let Some(t) = Images::new() else { return };
    let mut wrong = Vec::new();
    let exe = t.f.dir.join("pluxy-app");
    fs::write(&exe, &t.app).unwrap();
    let stale = elf(b"stale");
    appimage(
        &t.image,
        2,
        b"runtime",
        &[("usr/bin/pluxy-app", Entry::File(&stale))],
    );
    expect(
        &mut wrong,
        "an AppImage without the receipt, beside the app executable",
        t.validate(&[&exe, &t.image]),
        Some("matching build receipt"),
    );
    appimage(
        &t.image,
        2,
        b"runtime",
        &[
            ("usr/bin/pluxy-app", Entry::File(&stale)),
            (
                "usr/share/receipt.txt",
                Entry::File(env!("PLUXY_BUILD_RECEIPT").as_bytes()),
            ),
        ],
    );
    expect(
        &mut wrong,
        "the receipt only in a text file",
        t.validate(&[&t.image]),
        Some("matching build receipt"),
    );
    t.check(&[], b"runtime", 2).unwrap();
    expect(
        &mut wrong,
        "the clean AppImage beside the app executable",
        t.validate(&[&exe, &t.image]),
        None,
    );
    // Round 2: the receipt counts only in the package's app executable, usr/bin/pluxy-app.
    appimage(
        &t.image,
        2,
        b"runtime",
        &[
            ("usr/bin/pluxy-app", Entry::File(&stale)),
            ("usr/lib/libother.so", Entry::File(&t.app)),
        ],
    );
    expect(
        &mut wrong,
        "a stale app beside another ELF with the receipt",
        t.validate(&[&t.image]),
        Some("matching build receipt"),
    );
    expect(
        &mut wrong,
        "the clean AppImage",
        t.check(&[], b"runtime", 2),
        None,
    );
    assert!(wrong.is_empty(), "{wrong:#?}");
}

// Review finding 10: the unpacked folder is removed, read-only folders inside it too. Run in a
// child with its own TMPDIR, so whatever it leaves there is seen.
#[test]
fn release_config_removes_its_unpacked_folder() {
    const NAME: &str = "release_config_removes_its_unpacked_folder";
    if cfg!(feature = "artifact-test-trust") || cfg!(feature = "native-test") {
        return;
    }
    if std::env::var("PLUXY_RELEASE_TEST_CHILD").as_deref() != Ok(NAME) {
        let mut random = [0; 12];
        getrandom::fill(&mut random).unwrap();
        let tmp = std::env::temp_dir().join(format!(
            "pluxy-release-cleanup-{}",
            pluxy_app::artifact::hex(&random)
        ));
        fs::create_dir(&tmp).unwrap();
        let status = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", NAME, "--nocapture"])
            .env("TMPDIR", &tmp)
            .env("PLUXY_RELEASE_TEST_CHILD", NAME)
            .status()
            .unwrap();
        let left: Vec<_> = fs::read_dir(&tmp)
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .collect();
        let _ = std::process::Command::new("chmod")
            .arg("-R")
            .arg("u+rwx")
            .arg(&tmp)
            .status();
        fs::remove_dir_all(&tmp).unwrap();
        assert!(status.success(), "the child failed");
        assert!(left.is_empty(), "left in TMPDIR: {left:?}");
        return;
    }
    let t = Images::new().unwrap();
    t.check(
        &[
            ("usr/share/locked", Entry::Locked),
            ("usr/share/locked/file", Entry::File(b"x")),
        ],
        b"runtime",
        2,
    )
    .unwrap();
}
