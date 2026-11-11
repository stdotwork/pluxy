use pluxy_app::release_config::{validate, ReleaseInputs};
use serde_json::json;
use std::{fs, path::PathBuf};

#[test]
fn release_config_clean_and_contamination_controls() {
    let mut random = [0; 12];
    getrandom::fill(&mut random).unwrap();
    let dir = std::env::temp_dir().join(format!(
        "pluxy-release-{}",
        pluxy_app::artifact::hex(&random)
    ));
    fs::create_dir(&dir).unwrap();
    let bundled = dir.join("bundled");
    fs::create_dir(&bundled).unwrap();
    let inputs = ReleaseInputs {
        tauri_config: dir.join("tauri.json"),
        compiled_trust: dir.join("compiled.json"),
        packaged_trust: dir.join("packaged.json"),
        features: dir.join("features.json"),
        bundled_trust: bundled.clone(),
        artifacts: vec![PathBuf::from("pluxy.AppImage")],
    };
    let clean = || {
        fs::write(&inputs.tauri_config, include_bytes!("../tauri.conf.json")).unwrap();
        let trust = br#"{"roots":[],"updater":null}"#;
        fs::write(&inputs.compiled_trust, trust).unwrap();
        fs::write(&inputs.packaged_trust, trust).unwrap();
        fs::write(&inputs.features, b"[]").unwrap();
        for entry in fs::read_dir(&bundled).unwrap() {
            fs::remove_file(entry.unwrap().path()).unwrap();
        }
    };
    clean();
    if cfg!(feature = "artifact-test-trust") || cfg!(feature = "native-test") {
        assert!(validate(&inputs).is_err());
        fs::remove_dir_all(dir).unwrap();
        return;
    }
    validate(&inputs).unwrap();
    let mut private_seed = [0; 32];
    getrandom::fill(&mut private_seed).unwrap();
    let key = ed25519_dalek::SigningKey::from_bytes(&private_seed).verifying_key();
    let test_root = json!({"roots":[{"kind":"test","publisher":"example","source":"fixture","key_id":"runtime","public_key":pluxy_app::artifact::hex(key.as_bytes())}],"updater":null});
    for path in [
        &inputs.tauri_config,
        &inputs.compiled_trust,
        &inputs.packaged_trust,
        &inputs.features,
    ] {
        for bad in [
            test_root.clone(),
            json!({"features":["artifact-test-trust"]}),
            json!({"plugins":{"updater":{"pubkey":pluxy_app::artifact::hex(key.as_bytes())}}}),
            json!({"private_key":pluxy_app::artifact::hex(&private_seed)}),
        ] {
            clean();
            let mut input: serde_json::Value =
                serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
            if let Some(object) = input.as_object_mut() {
                object.extend(bad.as_object().unwrap().clone());
            } else {
                input.as_array_mut().unwrap().push(bad);
            }
            fs::write(path, serde_json::to_vec(&input).unwrap()).unwrap();
            assert!(
                validate(&inputs).is_err(),
                "accepted contamination in {}",
                path.display()
            );
            clean();
            validate(&inputs).unwrap();
        }
    }
    for bad in [
        test_root,
        json!({"features":["artifact-test-trust"]}),
        json!({"pubkey":pluxy_app::artifact::hex(key.as_bytes())}),
        json!({"private_key":pluxy_app::artifact::hex(&private_seed)}),
    ] {
        clean();
        fs::write(
            bundled.join("trust.json"),
            serde_json::to_vec(&bad).unwrap(),
        )
        .unwrap();
        assert!(validate(&inputs).is_err());
        clean();
        validate(&inputs).unwrap();
    }
    for (field, value) in [
        ("identifier", "dev.pluxy.desktop.test"),
        ("productName", "pluxy Test"),
        ("test_build", "true"),
    ] {
        clean();
        let mut config: serde_json::Value =
            serde_json::from_slice(&fs::read(&inputs.tauri_config).unwrap()).unwrap();
        config[field] = json!(value);
        fs::write(&inputs.tauri_config, serde_json::to_vec(&config).unwrap()).unwrap();
        assert!(validate(&inputs).is_err());
        clean();
        validate(&inputs).unwrap();
    }
    clean();
    fs::write(&inputs.features, br#"["native-test"]"#).unwrap();
    assert!(validate(&inputs).is_err());
    clean();
    fs::write(&inputs.features, br#"["artifact-test-trust"]"#).unwrap();
    assert!(validate(&inputs).is_err());
    clean();
    fs::write(bundled.join(".pluxy-test-trust-profile.json"), b"{}").unwrap();
    assert!(validate(&inputs).is_err());
    clean();
    let mut bad = inputs.clone();
    bad.artifacts = vec![PathBuf::from("pluxy-test.AppImage")];
    assert!(validate(&bad).is_err());
    clean();
    fs::write(bundled.join("key"), private_seed).unwrap();
    assert!(validate(&inputs).is_err());
    clean();
    validate(&inputs).unwrap();
    fs::remove_dir_all(dir).unwrap();
}
