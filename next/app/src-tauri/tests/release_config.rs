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
            artifacts: vec![dir.join("pluxy.AppImage")],
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
        fs::write(&self.inputs.artifacts[0], env!("PLUXY_BUILD_RECEIPT")).unwrap();
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
        fs::write(&f.inputs.artifacts[0], bytes).unwrap();
        f.refused("test build marker/identity");
    }
    fs::write(&f.inputs.artifacts[0], b"stale-build-receipt").unwrap();
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
}

/// A fixture AppImage at path: a 64-byte ELF64 header with the AppImage magic and type at offset
/// 8 and its section header table ending at runtime.len() (no sections), then the runtime bytes
/// after the header, then a squashfs image of files made by mksquashfs (squashfs-tools). It is
/// never run: the validator must find the image from the header alone.
fn appimage(path: &std::path::Path, kind: u8, runtime: &[u8], files: &[(&str, Entry)]) {
    let dir = path.with_extension("AppDir");
    for (name, entry) in files {
        let file = dir.join(name);
        fs::create_dir_all(file.parent().unwrap()).unwrap();
        match entry {
            Entry::File(bytes) => fs::write(&file, bytes).unwrap(),
            Entry::Link(target) => std::os::unix::fs::symlink(target, &file).unwrap(),
        }
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
    fs::remove_dir_all(&dir).unwrap();
    fs::remove_file(&image).unwrap();
}

// An AppImage is unpacked and every file in it checked; each refusal has the clean AppImage on
// the same route as its positive control.
#[test]
fn release_config_checks_inside_appimages() {
    let f = Fixture::new();
    if cfg!(feature = "artifact-test-trust") || cfg!(feature = "native-test") {
        return; // the validator refuses test builds before any artifact (test above)
    }
    let receipt = env!("PLUXY_BUILD_RECEIPT").as_bytes();
    let image = f.dir.join("pluxy.AppImage");
    let mut inputs = f.inputs.clone();
    inputs.artifacts = vec![image.clone()];
    let mut root = serde_json::to_vec(&json!({"roots":[{"kind":"test","publisher":"example","source":"fixture","key_id":"runtime","public_key":"00".repeat(32)}],"updater":null})).unwrap();
    let clean: Vec<(&str, Entry)> = vec![
        ("AppRun", Entry::File(b"#!/bin/sh\n")),
        ("usr/bin/pluxy-app", Entry::File(receipt)),
        // Bundled system libraries may have names a release input may not.
        ("usr/lib/libsecret-1.so.0", Entry::File(b"library")),
        (
            "usr/share/trust/app-package-trust.json",
            Entry::File(br#"{"roots":[],"updater":null}"#),
        ),
        (
            "pluxy.desktop",
            Entry::Link("usr/share/applications/pluxy.desktop"),
        ),
    ];
    let check = |extra: Vec<(&str, Entry)>, runtime: &[u8], kind: u8, reason: Option<&str>| {
        let mut files = clean.clone();
        files.extend(extra);
        appimage(&image, kind, runtime, &files);
        let result = validate(&inputs);
        match reason {
            None => result.unwrap(),
            Some(reason) => {
                let error = result.unwrap_err();
                assert!(error.contains(reason), "expected {reason}, got {error}");
            }
        }
    };
    let clean_control = || check(vec![], b"runtime", 2, None);
    clean_control();
    let marker = b"..PLUXY_TEST_BUILD_DO_NOT_RELEASE..";
    check(
        vec![("usr/lib/libmarked.so", Entry::File(marker))],
        b"runtime",
        2,
        Some("pluxy.AppImage!usr/lib/libmarked.so: test build marker/identity"),
    );
    clean_control();
    check(
        vec![(
            "usr/share/applications/pluxy.desktop",
            Entry::File(b"Name=pluxy Test\n"),
        )],
        b"runtime",
        2,
        Some("test build marker/identity"),
    );
    clean_control();
    check(
        vec![(
            "usr/share/key.pub",
            Entry::File(b"dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWdu"),
        )],
        b"runtime",
        2,
        Some("updater key or signature"),
    );
    clean_control();
    check(
        vec![("usr/bin/pluxy-app-test", Entry::File(b"x"))],
        b"runtime",
        2,
        Some("test file name in package"),
    );
    clean_control();
    check(
        vec![("icon", Entry::Link("pluxy Test.png"))],
        b"runtime",
        2,
        Some("link to a test file name"),
    );
    clean_control();
    root.push(b'\n');
    check(
        vec![("usr/share/roots.json", Entry::File(&root))],
        b"runtime",
        2,
        Some("test root"),
    );
    clean_control();
    // The runtime part is checked as bytes, before the image.
    check(
        vec![],
        marker,
        2,
        Some("pluxy.AppImage: test build marker/identity"),
    );
    clean_control();
    check(
        vec![],
        b"runtime",
        1,
        Some("AppImage type 1 cannot be checked"),
    );
    clean_control();
    // A header that does not end where the image starts is refused, not guessed.
    let mut bytes = fs::read(&image).unwrap();
    bytes[0x28] += 1;
    fs::write(&image, bytes).unwrap();
    assert!(validate(&inputs)
        .unwrap_err()
        .contains("no squashfs image where its runtime ends"));
    clean_control();
    // Without the receipt anywhere inside, the AppImage does not describe this build.
    appimage(
        &image,
        2,
        b"runtime",
        &[("AppRun", Entry::File(b"#!/bin/sh\n"))],
    );
    assert!(validate(&inputs)
        .unwrap_err()
        .contains("matching build receipt"));
    clean_control();
}
