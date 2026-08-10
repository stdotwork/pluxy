use super::*;

#[tauri::command]
pub(crate) fn open_external_url(app: tauri::AppHandle, url: String) -> Result<(), String> {
    open_external_url_inner(&app, &url)
}

#[tauri::command]
pub(crate) async fn check_app_update(
    state: tauri::State<'_, AppUpdateState>,
    gui_config_state: tauri::State<'_, GuiConfigState>,
) -> Result<AppUpdateInfo, String> {
    let proxy_url = gui_config_state.snapshot()?.proxy_url;
    let client = build_http_client_with_proxy(
        reqwest::Client::builder()
            .redirect(release_https_redirect_policy())
            .connect_timeout(Duration::from_secs(8))
            .read_timeout(Duration::from_secs(15))
            .timeout(Duration::from_secs(20)),
        &proxy_url,
        "创建版本检查客户端失败",
    )?;
    let manifest = fetch_portable_update_manifest(&client).await?;
    let latest_version = normalize_version(&manifest.version);
    let current_version = normalize_version(env!("CARGO_PKG_VERSION"));
    let update_available = is_app_update_available(&current_version, &latest_version)?;
    let target = portable_update_target();
    let asset_catalog = manifest.full_assets.as_ref().unwrap_or(&manifest.assets);
    let asset = target.and_then(|(key, _)| asset_catalog.get(key)).cloned();
    let portable_support = target
        .map(|(_, arch)| validate_local_portable_app_manifest(arch))
        .transpose()?;
    let auto_update_supported = cfg!(windows) && portable_support == Some(true) && asset.is_some();
    let unsupported_reason = if auto_update_supported {
        None
    } else if !cfg!(windows) {
        Some("应用内自动升级当前仅支持 Windows 便携版".to_string())
    } else if portable_support != Some(true) {
        Some("当前程序不是支持自动升级的便携版，请手动下载首个支持版本".to_string())
    } else {
        Some("更新清单不包含当前 Windows 架构".to_string())
    };

    let pending = if update_available && auto_update_supported {
        let (_, arch) = target.expect("portable target checked above");
        Some(PendingAppUpdate {
            version: latest_version.clone(),
            asset: asset.clone().expect("portable asset checked above"),
            arch: arch.to_string(),
        })
    } else {
        None
    };
    state.set_pending(
        pending,
        AppUpdateTask {
            phase: if update_available {
                "available".to_string()
            } else {
                "idle".to_string()
            },
            target_version: update_available.then(|| latest_version.clone()),
            total_bytes: asset.as_ref().map(|value| value.size_bytes),
            ..AppUpdateTask::default()
        },
    );

    Ok(AppUpdateInfo {
        current_version,
        latest_version,
        update_available,
        release_url: manifest.release_url,
        auto_update_supported,
        download_size_bytes: asset.map(|value| value.size_bytes),
        unsupported_reason,
    })
}

pub(crate) async fn fetch_portable_update_manifest(
    client: &reqwest::Client,
) -> Result<PortableUpdateManifest, String> {
    match fetch_portable_update_manifest_url(client, APP_UPDATE_MANIFEST_URL).await {
        Ok(manifest) => Ok(manifest),
        Err(github_error) => {
            let Some(repository) = configured_gitcode_gui_repository() else {
                return Err(github_error);
            };
            let fallback = async {
                let release_url =
                    format!("https://api.gitcode.com/api/v5/repos/{repository}/releases/latest");
                let release = client
                    .get(release_url)
                    .header(reqwest::header::ACCEPT, "application/json")
                    .header(reqwest::header::USER_AGENT, APP_USER_AGENT)
                    .send()
                    .await
                    .map_err(|error| format!("query GitCode latest release: {error}"))?
                    .error_for_status()
                    .map_err(|error| format!("read GitCode latest release: {error}"))?
                    .json::<GitcodeRelease>()
                    .await
                    .map_err(|error| format!("parse GitCode latest release: {error}"))?;
                validate_release_tag(&release.tag_name)?;
                let manifest_url = gitcode_release_attachment_url(
                    repository,
                    &release.tag_name,
                    APP_UPDATE_MANIFEST_NAME,
                );
                fetch_portable_update_manifest_url(client, &manifest_url).await
            }
            .await;
            fallback.map_err(|gitcode_error| {
                format!(
                    "GitHub update source failed: {github_error}; GitCode fallback failed: {gitcode_error}"
                )
            })
        }
    }
}

pub(crate) async fn fetch_portable_update_manifest_url(
    client: &reqwest::Client,
    url: &str,
) -> Result<PortableUpdateManifest, String> {
    let manifest = client
        .get(url)
        .header(reqwest::header::ACCEPT, "application/json")
        .header(reqwest::header::USER_AGENT, APP_USER_AGENT)
        .send()
        .await
        .map_err(|error| format!("request update manifest: {error}"))?
        .error_for_status()
        .map_err(|error| format!("read update manifest: {error}"))?
        .json::<PortableUpdateManifest>()
        .await
        .map_err(|error| format!("parse update manifest: {error}"))?;
    validate_portable_update_manifest(&manifest)?;
    Ok(manifest)
}

pub(crate) fn configured_gitcode_gui_repository() -> Option<&'static str> {
    configured_gitcode_repository(option_env!("GITCODE_GUI_REPOSITORY"))
}

pub(crate) fn configured_gitcode_core_repository() -> Option<&'static str> {
    configured_gitcode_repository(option_env!("GITCODE_CORE_REPOSITORY"))
}

pub(crate) fn configured_gitcode_repository(
    repository: Option<&'static str>,
) -> Option<&'static str> {
    let repository = repository?.trim();
    let mut parts = repository.split('/');
    let valid_part = |part: &str| {
        !part.is_empty()
            && part
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'.' | b'-'))
    };
    match (parts.next(), parts.next(), parts.next()) {
        (Some(owner), Some(repo), None) if valid_part(owner) && valid_part(repo) => {
            Some(repository)
        }
        _ => None,
    }
}

pub(crate) fn validate_release_tag(tag: &str) -> Result<(), String> {
    semver::Version::parse(tag.strip_prefix('v').unwrap_or(tag))
        .map(|_| ())
        .map_err(|error| format!("invalid release tag {tag}: {error}"))
}

pub(crate) fn gitcode_release_attachment_url(
    repository: &str,
    tag: &str,
    filename: &str,
) -> String {
    format!(
        "https://api.gitcode.com/api/v5/repos/{repository}/releases/{tag}/attach_files/{filename}/download"
    )
}

pub(crate) fn release_https_redirect_policy() -> reqwest::redirect::Policy {
    reqwest::redirect::Policy::custom(|attempt| {
        let url = attempt.url();
        let trusted_host = matches!(
            url.host_str(),
            Some(
                "github.com"
                    | "objects.githubusercontent.com"
                    | "release-assets.githubusercontent.com"
                    | "api.gitcode.com"
                    | "gitcode.com"
                    | "file-cdn.gitcode.com"
            )
        );
        if url.scheme() == "https"
            && url.port().is_none()
            && url.username().is_empty()
            && url.password().is_none()
            && trusted_host
        {
            attempt.follow()
        } else {
            attempt.stop()
        }
    })
}

pub(crate) fn github_https_redirect_policy() -> reqwest::redirect::Policy {
    reqwest::redirect::Policy::custom(|attempt| {
        let url = attempt.url();
        let trusted_host = matches!(
            url.host_str(),
            Some(
                "github.com"
                    | "raw.githubusercontent.com"
                    | "objects.githubusercontent.com"
                    | "release-assets.githubusercontent.com"
            )
        );
        if url.scheme() == "https"
            && url.port().is_none()
            && url.username().is_empty()
            && url.password().is_none()
            && trusted_host
        {
            attempt.follow()
        } else {
            attempt.stop()
        }
    })
}

pub(crate) fn validate_portable_update_manifest(
    manifest: &PortableUpdateManifest,
) -> Result<(), String> {
    if manifest.schema_version != 1 {
        return Err(format!(
            "不支持的软件更新清单版本: {}",
            manifest.schema_version
        ));
    }
    semver::Version::parse(manifest.version.trim().trim_start_matches('v'))
        .map_err(|error| format!("软件更新版本无效: {error}"))?;
    chrono::DateTime::parse_from_rfc3339(manifest.published_at.trim())
        .map_err(|error| format!("软件更新发布时间无效: {error}"))?;
    let release_url = reqwest::Url::parse(&manifest.release_url)
        .map_err(|_| "软件更新发布地址无效".to_string())?;
    if release_url.scheme() != "https"
        || release_url.host_str() != Some("github.com")
        || release_url.port().is_some()
        || !release_url.username().is_empty()
        || release_url.password().is_some()
        || release_url.query().is_some()
        || release_url.fragment().is_some()
        || !release_url
            .path()
            .starts_with("/router-for-me/EasyCLIProxyAPI/releases/tag/v")
    {
        return Err("软件更新发布地址不受信任".to_string());
    }
    if manifest.assets.len() != 2 {
        return Err("软件更新清单必须包含两个 Windows 架构".to_string());
    }
    for (key, arch) in [("windows-amd64", "amd64"), ("windows-aarch64", "aarch64")] {
        let asset = manifest
            .assets
            .get(key)
            .ok_or_else(|| format!("软件更新清单缺少 {key}"))?;
        validate_portable_update_asset(asset)?;
        let full_package_name = format!(
            "EasyCLIProxyAPI-v{}-Windows-{arch}.zip",
            manifest.version.trim().trim_start_matches('v')
        );
        let full_package_url = format!(
            "{APP_RELEASE_DOWNLOAD_PREFIX}v{}/{}",
            manifest.version.trim().trim_start_matches('v'),
            full_package_name
        );
        let legacy_package_name = format!(
            "EasyCLIProxyAPI-update-v{}-Windows-{arch}.zip",
            manifest.version.trim().trim_start_matches('v')
        );
        let legacy_package_url = format!(
            "{APP_RELEASE_DOWNLOAD_PREFIX}v{}/{}",
            manifest.version.trim().trim_start_matches('v'),
            legacy_package_name
        );
        if asset.url != full_package_url && asset.url != legacy_package_url {
            return Err(format!("软件更新资产名称与 {key} 不匹配"));
        }
        validate_portable_update_asset_fallbacks(
            asset,
            &format!("v{}", manifest.version.trim().trim_start_matches('v')),
            &[&full_package_name, &legacy_package_name],
        )?;
    }
    if let Some(full_assets) = &manifest.full_assets {
        if full_assets.len() != 2 {
            return Err(
                "Full update asset catalog must contain both Windows architectures".to_string(),
            );
        }
        for (key, arch) in [("windows-amd64", "amd64"), ("windows-aarch64", "aarch64")] {
            let asset = full_assets
                .get(key)
                .ok_or_else(|| format!("Full update asset catalog is missing {key}"))?;
            validate_portable_update_asset(asset)?;
            let package_name = format!(
                "EasyCLIProxyAPI-v{}-Windows-{arch}.zip",
                manifest.version.trim().trim_start_matches('v')
            );
            let expected_url = format!(
                "{APP_RELEASE_DOWNLOAD_PREFIX}v{}/{}",
                manifest.version.trim().trim_start_matches('v'),
                package_name
            );
            if asset.url != expected_url {
                return Err(format!("Full update asset name does not match {key}"));
            }
            validate_portable_update_asset_fallbacks(
                asset,
                &format!("v{}", manifest.version.trim().trim_start_matches('v')),
                &[&package_name],
            )?;
        }
    }
    Ok(())
}

pub(crate) fn validate_portable_update_asset(asset: &PortableUpdateAsset) -> Result<(), String> {
    let url = reqwest::Url::parse(&asset.url).map_err(|_| "软件更新下载地址无效".to_string())?;
    if url.scheme() != "https"
        || url.host_str() != Some("github.com")
        || url.port().is_some()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
        || !asset.url.starts_with(APP_RELEASE_DOWNLOAD_PREFIX)
    {
        return Err("软件更新下载地址不受信任".to_string());
    }
    if asset.size_bytes == 0 || asset.size_bytes > 512 * 1024 * 1024 {
        return Err("软件更新包大小无效".to_string());
    }
    let digest = asset.sha256.trim().to_ascii_lowercase();
    if digest.len() != 64 || !digest.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err("软件更新 SHA-256 无效".to_string());
    }
    Ok(())
}

pub(crate) fn validate_portable_update_asset_fallbacks(
    asset: &PortableUpdateAsset,
    tag: &str,
    expected_filenames: &[&str],
) -> Result<(), String> {
    validate_portable_update_asset_fallbacks_for_repository(
        asset,
        tag,
        expected_filenames,
        configured_gitcode_gui_repository(),
    )
}

pub(crate) fn validate_portable_update_asset_fallbacks_for_repository(
    asset: &PortableUpdateAsset,
    tag: &str,
    expected_filenames: &[&str],
    repository: Option<&str>,
) -> Result<(), String> {
    if asset.fallback_urls.is_empty() {
        return Ok(());
    }
    let repository = repository
        .ok_or_else(|| "GitCode fallback is not configured for this build".to_string())?;
    if asset.fallback_urls.len() != 1 {
        return Err("Software update assets may define only one fallback URL".to_string());
    }
    let fallback = &asset.fallback_urls[0];
    let trusted = expected_filenames
        .iter()
        .any(|filename| fallback == &gitcode_release_attachment_url(repository, tag, filename));
    if !trusted {
        return Err("Software update fallback URL is not trusted".to_string());
    }
    Ok(())
}

pub(crate) fn portable_update_target() -> Option<(&'static str, &'static str)> {
    if !cfg!(windows) {
        return None;
    }
    match env::consts::ARCH {
        "x86_64" => Some(("windows-amd64", "amd64")),
        "aarch64" => Some(("windows-aarch64", "aarch64")),
        _ => None,
    }
}

pub(crate) fn validate_local_portable_app_manifest(expected_arch: &str) -> Result<bool, String> {
    let path = executable_dir()?.join(PORTABLE_APP_MANIFEST_FILE);
    if !path.is_file() {
        return Ok(false);
    }
    let contents =
        fs::read_to_string(&path).map_err(|error| format!("读取便携版标识失败: {error}"))?;
    let manifest = serde_json::from_str::<PortableAppManifest>(&contents)
        .map_err(|error| format!("解析便携版标识失败: {error}"))?;
    Ok(manifest.schema_version == 1
        && manifest.application == "EasyCLIProxyAPI"
        && manifest.platform == "windows"
        && manifest.arch == expected_arch
        && manifest.auto_update
        && normalize_version(&manifest.version) == normalize_version(env!("CARGO_PKG_VERSION")))
}

#[tauri::command]
pub(crate) fn get_app_update_task(state: tauri::State<'_, AppUpdateState>) -> AppUpdateTask {
    state.snapshot()
}

#[tauri::command]
pub(crate) fn cancel_app_update(state: tauri::State<'_, AppUpdateState>) -> Result<(), String> {
    let task = state.snapshot();
    if !task.running || !task.cancellable {
        return Err("当前应用更新阶段无法取消".to_string());
    }
    state.cancel();
    Ok(())
}

#[tauri::command]
pub(crate) async fn start_app_update(
    app: tauri::AppHandle,
    state: tauri::State<'_, AppUpdateState>,
    gui_config_state: tauri::State<'_, GuiConfigState>,
) -> Result<(), String> {
    if !cfg!(windows) {
        return Err("应用内自动升级当前仅支持 Windows 便携版".to_string());
    }
    let proxy_url = gui_config_state.snapshot()?.proxy_url;
    let token = CancellationToken::new();
    let pending = state.start(token.clone())?;
    let task = state.snapshot();
    let _ = app.emit(APP_UPDATE_PROGRESS_EVENT, task);
    let update_app = app.clone();
    tauri::async_runtime::spawn(async move {
        let outcome =
            download_and_stage_portable_app_update(&update_app, &pending, &token, &proxy_url).await;
        if let Err(error) = outcome {
            let state = update_app.state::<AppUpdateState>();
            let cancelled = token.is_cancelled();
            let task = state.finish(
                if cancelled { "cancelled" } else { "failed" },
                Some(if cancelled {
                    "应用更新下载已取消".to_string()
                } else {
                    error
                }),
            );
            let _ = update_app.emit(APP_UPDATE_PROGRESS_EVENT, task);
        }
    });
    Ok(())
}

pub(crate) async fn download_and_stage_portable_app_update(
    app: &tauri::AppHandle,
    pending: &PendingAppUpdate,
    token: &CancellationToken,
    proxy_url: &str,
) -> Result<(), String> {
    #[cfg(not(windows))]
    {
        let _ = (app, pending, token, proxy_url);
        Err("应用内自动升级当前仅支持 Windows 便携版".to_string())
    }

    #[cfg(windows)]
    {
        validate_portable_update_asset(&pending.asset)?;
        let work_dir = env::temp_dir().join(format!(
            "EasyCLIProxyAPI-update-{}-{}-{}",
            pending.version,
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos()
        ));
        fs::create_dir_all(&work_dir)
            .map_err(|error| format!("创建应用更新临时目录失败: {error}"))?;
        let archive_path = work_dir.join("update.zip");
        let result = async {
            download_portable_update_archive(app, pending, token, &archive_path, proxy_url).await?;
            if token.is_cancelled() {
                return Err("应用更新下载已取消".to_string());
            }

            update_app_task(app, |task| {
                task.cancellable = false;
                task.phase = "verifying".to_string();
                task.message = Some("正在校验应用更新包".to_string());
            });
            let actual_sha256 = sha256_file(&archive_path)?;
            if actual_sha256 != pending.asset.sha256.trim().to_ascii_lowercase() {
                return Err("应用更新包 SHA-256 校验失败".to_string());
            }

            update_app_task(app, |task| {
                task.phase = "staging".to_string();
                task.message = Some("正在准备应用更新".to_string());
            });
            let staging_dir = work_dir.join("staging");
            let package = extract_portable_update_archive(&archive_path, &staging_dir)?;
            if normalize_version(&package.manifest.version) != normalize_version(&pending.version)
                || package.manifest.platform != "windows"
                || package.manifest.arch != pending.arch
            {
                return Err("应用更新包版本或架构不匹配".to_string());
            }

            let current_exe =
                env::current_exe().map_err(|error| format!("读取当前程序路径失败: {error}"))?;
            let app_dir = current_exe
                .parent()
                .ok_or_else(|| "当前程序路径没有父目录".to_string())?;
            preflight_portable_update_directory(app_dir)?;
            let core_archive_name = match package.core_archive_name {
                Some(name) => name,
                None => stage_current_portable_core_payload(app_dir, &staging_dir, &pending.arch)?,
            };
            let helper_path = work_dir.join("EasyCLIProxyAPI-updater.exe");
            fs::copy(&current_exe, &helper_path)
                .map_err(|error| format!("准备应用更新助手失败: {error}"))?;

            let descriptor = PortableUpdateDescriptor {
                parent_pid: std::process::id(),
                current_exe: current_exe.clone(),
                staged_exe: staging_dir.join(PORTABLE_APP_BINARY),
                current_manifest: app_dir.join(PORTABLE_APP_MANIFEST_FILE),
                staged_manifest: staging_dir.join(PORTABLE_APP_MANIFEST_FILE),
                backup_exe: app_dir.join(".EasyCLIProxyAPI.exe.update-backup"),
                backup_manifest: app_dir.join(".portable-app.json.update-backup"),
                current_core_version: app_dir.join(CORE_VERSION_FILE),
                staged_core_version: staging_dir.join(CORE_VERSION_FILE),
                backup_core_version: app_dir.join(".core-version.txt.update-backup"),
                staged_core_archive: staging_dir.join("cpa-core").join(&core_archive_name),
                target_core_archive: app_dir.join("cpa-core").join(&core_archive_name),
                install_core_archive: !app_dir.join("cpa-core").join(&core_archive_name).is_file(),
                ack_path: work_dir.join("update-started.ack"),
                work_dir: work_dir.clone(),
                target_version: pending.version.clone(),
            };
            let descriptor_path = work_dir.join("update-descriptor.json");
            fs::write(
                &descriptor_path,
                serde_json::to_vec_pretty(&descriptor)
                    .map_err(|error| format!("序列化应用更新描述失败: {error}"))?,
            )
            .map_err(|error| format!("写入应用更新描述失败: {error}"))?;

            let mut command = Command::new(&helper_path);
            command
                .arg("--portable-update-helper")
                .arg(&descriptor_path)
                .stdin(Stdio::null())
                .stdout(Stdio::null())
                .stderr(Stdio::null());
            configure_background_command(&mut command);
            command
                .spawn()
                .map_err(|error| format!("启动应用更新助手失败: {error}"))?;

            update_app_task(app, |task| {
                task.cancellable = false;
                task.phase = "restarting".to_string();
                task.message = Some("更新已准备完成，应用即将重启".to_string());
            });
            tokio::time::sleep(Duration::from_millis(350)).await;
            app.exit(0);
            Ok(())
        }
        .await;

        if result.is_err() {
            let _ = fs::remove_dir_all(&work_dir);
        }
        result
    }
}

#[cfg(windows)]
pub(crate) async fn download_portable_update_archive(
    app: &tauri::AppHandle,
    pending: &PendingAppUpdate,
    token: &CancellationToken,
    destination: &Path,
    proxy_url: &str,
) -> Result<(), String> {
    let client = build_http_client_with_proxy(
        reqwest::Client::builder()
            .redirect(release_https_redirect_policy())
            .connect_timeout(Duration::from_secs(15))
            .read_timeout(Duration::from_secs(30))
            .timeout(Duration::from_secs(15 * 60)),
        proxy_url,
        "创建应用更新下载客户端失败",
    )?;
    let urls = std::iter::once(&pending.asset.url)
        .chain(pending.asset.fallback_urls.iter())
        .collect::<Vec<_>>();
    let mut failures = Vec::new();
    for (index, url) in urls.iter().enumerate() {
        update_app_task(app, |task| {
            task.downloaded_bytes = 0;
            task.percent = Some(0.0);
            if index > 0 {
                task.message = Some("GitHub 下载失败，正在切换到 GitCode".to_string());
            }
        });
        match download_portable_update_archive_url(app, pending, token, destination, &client, url)
            .await
        {
            Ok(()) => return Ok(()),
            Err(error) if token.is_cancelled() => return Err(error),
            Err(error) => failures.push(error),
        }
    }
    Err(format!("所有应用更新下载源均失败: {}", failures.join("; ")))
}

#[cfg(windows)]
pub(crate) async fn download_portable_update_archive_url(
    app: &tauri::AppHandle,
    pending: &PendingAppUpdate,
    token: &CancellationToken,
    destination: &Path,
    client: &reqwest::Client,
    url: &str,
) -> Result<(), String> {
    let response = client
        .get(url)
        .header(reqwest::header::ACCEPT, "application/octet-stream")
        .header(reqwest::header::USER_AGENT, APP_USER_AGENT)
        .send()
        .await
        .map_err(|error| format!("下载应用更新失败: {error}"))?
        .error_for_status()
        .map_err(|error| format!("下载应用更新失败: {error}"))?;
    let mut stream = response.bytes_stream();
    let mut file =
        File::create(destination).map_err(|error| format!("创建应用更新临时文件失败: {error}"))?;
    let mut downloaded = 0_u64;
    while let Some(chunk) = stream.next().await {
        if token.is_cancelled() {
            return Err("应用更新下载已取消".to_string());
        }
        let chunk = chunk.map_err(|error| format!("读取应用更新下载数据失败: {error}"))?;
        file.write_all(&chunk)
            .map_err(|error| format!("写入应用更新临时文件失败: {error}"))?;
        downloaded = downloaded.saturating_add(chunk.len() as u64);
        if downloaded > pending.asset.size_bytes {
            return Err("应用更新包超过清单声明大小".to_string());
        }
        update_app_task(app, |task| {
            task.downloaded_bytes = downloaded;
            task.total_bytes = Some(pending.asset.size_bytes);
            task.percent = Some((downloaded as f64 / pending.asset.size_bytes as f64) * 100.0);
            task.message = Some(format!(
                "{} / {}",
                format_byte_count(downloaded),
                format_byte_count(pending.asset.size_bytes)
            ));
        });
    }
    file.flush()
        .map_err(|error| format!("保存应用更新临时文件失败: {error}"))?;
    if downloaded != pending.asset.size_bytes {
        return Err(format!(
            "应用更新包大小不匹配: 预期 {}，实际 {}",
            pending.asset.size_bytes, downloaded
        ));
    }
    Ok(())
}

#[cfg(windows)]
pub(crate) fn update_app_task<F>(app: &tauri::AppHandle, update: F)
where
    F: FnOnce(&mut AppUpdateTask),
{
    let state = app.state::<AppUpdateState>();
    let task = state.update_task(update);
    let _ = app.emit(APP_UPDATE_PROGRESS_EVENT, task);
}

#[cfg(windows)]
pub(crate) fn format_byte_count(bytes: u64) -> String {
    const UNITS: [&str; 4] = ["B", "KB", "MB", "GB"];
    let mut value = bytes as f64;
    let mut unit = 0_usize;
    while value >= 1024.0 && unit < UNITS.len() - 1 {
        value /= 1024.0;
        unit += 1;
    }
    if unit == 0 {
        format!("{bytes} {}", UNITS[unit])
    } else {
        format!("{value:.1} {}", UNITS[unit])
    }
}

#[cfg(windows)]
pub(crate) fn extract_portable_update_archive(
    archive_path: &Path,
    staging_dir: &Path,
) -> Result<PortablePackagePayload, String> {
    fs::create_dir_all(staging_dir)
        .map_err(|error| format!("创建应用更新暂存目录失败: {error}"))?;
    let file = File::open(archive_path).map_err(|error| format!("打开应用更新包失败: {error}"))?;
    let mut archive =
        ZipArchive::new(file).map_err(|error| format!("读取应用更新 ZIP 失败: {error}"))?;
    let archive_names = archive
        .file_names()
        .map(|name| name.replace('\\', "/"))
        .collect::<Vec<_>>();
    let legacy_layout = archive_names.len() == 2
        && archive_names.iter().any(|name| name == PORTABLE_APP_BINARY)
        && archive_names
            .iter()
            .any(|name| name == PORTABLE_APP_MANIFEST_FILE);
    let mut package_root = None::<String>;
    let mut seen_binary = false;
    let mut seen_manifest = false;
    let mut seen_core_version = false;
    let mut core_archive_name = None::<String>;
    let mut regular_file_count = 0_u32;
    for index in 0..archive.len() {
        let mut entry = archive
            .by_index(index)
            .map_err(|error| format!("读取应用更新 ZIP 条目失败: {error}"))?;
        let name = entry.name().replace('\\', "/");
        if entry.enclosed_name().is_none() {
            return Err("应用更新包包含不安全路径".to_string());
        }
        if entry
            .unix_mode()
            .is_some_and(|mode| mode & 0o170000 == 0o120000)
        {
            return Err("应用更新包不能包含符号链接".to_string());
        }
        if entry.is_dir() {
            continue;
        }
        regular_file_count = regular_file_count.saturating_add(1);
        let relative;
        let relative_name;
        if legacy_layout {
            relative = Vec::new();
            relative_name = name.clone();
        } else {
            let mut components = name.split('/');
            let root = components
                .next()
                .filter(|value| !value.is_empty())
                .ok_or_else(|| "应用更新包缺少顶层目录".to_string())?;
            relative = components.collect::<Vec<_>>();
            if relative.is_empty() || relative.iter().any(|value| value.is_empty()) {
                return Err("应用更新包目录结构无效".to_string());
            }
            if package_root.as_deref().is_some_and(|value| value != root) {
                return Err("应用更新包必须只包含一个顶层目录".to_string());
            }
            package_root.get_or_insert_with(|| root.to_string());
            relative_name = relative.join("/");
        }
        let destination = match relative_name.as_str() {
            PORTABLE_APP_BINARY if !seen_binary => {
                if entry.size() == 0 || entry.size() > 256 * 1024 * 1024 {
                    return Err("应用更新程序大小异常".to_string());
                }
                seen_binary = true;
                staging_dir.join(PORTABLE_APP_BINARY)
            }
            PORTABLE_APP_MANIFEST_FILE if !seen_manifest => {
                if entry.size() == 0 || entry.size() > 64 * 1024 {
                    return Err("应用更新标识大小异常".to_string());
                }
                seen_manifest = true;
                staging_dir.join(PORTABLE_APP_MANIFEST_FILE)
            }
            CORE_VERSION_FILE if !seen_core_version => {
                if entry.size() == 0 || entry.size() > 1024 {
                    return Err("应用更新包内核版本文件大小异常".to_string());
                }
                seen_core_version = true;
                staging_dir.join(CORE_VERSION_FILE)
            }
            _ if relative.len() == 2
                && relative[0] == "cpa-core"
                && core_archive_name.is_none() =>
            {
                let filename = relative[1];
                if !filename.starts_with("CLIProxyAPI_")
                    || !filename.ends_with(".zip")
                    || filename.contains("_no-plugin")
                    || entry.size() == 0
                    || entry.size() > 512 * 1024 * 1024
                {
                    return Err("应用更新包内置内核压缩包无效".to_string());
                }
                core_archive_name = Some(filename.to_string());
                let core_staging_dir = staging_dir.join("cpa-core");
                fs::create_dir_all(&core_staging_dir)
                    .map_err(|error| format!("创建内置内核暂存目录失败: {error}"))?;
                core_staging_dir.join(filename)
            }
            _ => return Err(format!("应用更新包包含未知文件: {name}")),
        };
        let mut output = File::create(&destination)
            .map_err(|error| format!("创建应用更新暂存文件失败: {error}"))?;
        io::copy(&mut entry, &mut output)
            .map_err(|error| format!("解压应用更新文件失败: {error}"))?;
        output
            .flush()
            .map_err(|error| format!("保存应用更新暂存文件失败: {error}"))?;
    }
    let required_files_present = if legacy_layout {
        regular_file_count == 2 && seen_binary && seen_manifest
    } else {
        regular_file_count == 4 && seen_binary && seen_manifest && seen_core_version
    };
    if !required_files_present {
        return Err("应用更新包缺少必要文件".to_string());
    }
    let manifest = fs::read_to_string(staging_dir.join(PORTABLE_APP_MANIFEST_FILE))
        .map_err(|error| format!("读取应用更新标识失败: {error}"))?;
    let manifest = serde_json::from_str::<PortableAppManifest>(&manifest)
        .map_err(|error| format!("解析应用更新标识失败: {error}"))?;
    if manifest.schema_version != 1
        || manifest.application != "EasyCLIProxyAPI"
        || !manifest.auto_update
    {
        return Err("应用更新标识无效".to_string());
    }
    if !legacy_layout {
        let expected_root = format!(
            "EasyCLIProxyAPI-v{}-Windows-{}",
            manifest.version.trim().trim_start_matches('v'),
            manifest.arch
        );
        if package_root.as_deref() != Some(expected_root.as_str()) {
            return Err("应用更新包顶层目录与版本或架构不匹配".to_string());
        }
        let core_version = fs::read_to_string(staging_dir.join(CORE_VERSION_FILE))
            .map_err(|error| format!("读取内置内核版本失败: {error}"))?;
        let core_version = core_version.trim().trim_start_matches('v');
        if core_version.is_empty()
            || !core_version.split('.').all(|segment| {
                !segment.is_empty() && segment.bytes().all(|byte| byte.is_ascii_digit())
            })
        {
            return Err("应用更新包内置内核版本无效".to_string());
        }
        let expected_core_archive =
            format!("CLIProxyAPI_{core_version}_windows_{}.zip", manifest.arch);
        if core_archive_name.as_deref() != Some(expected_core_archive.as_str()) {
            return Err("应用更新包内置内核版本或架构不匹配".to_string());
        }
    }
    Ok(PortablePackagePayload {
        manifest,
        core_archive_name,
    })
}

#[cfg(windows)]
pub(crate) fn stage_current_portable_core_payload(
    app_dir: &Path,
    staging_dir: &Path,
    arch: &str,
) -> Result<String, String> {
    let current_core_version = app_dir.join(CORE_VERSION_FILE);
    let core_version = fs::read_to_string(&current_core_version)
        .map_err(|error| format!("读取当前内置内核版本失败: {error}"))?;
    let core_version = core_version.trim().trim_start_matches('v');
    if core_version.is_empty()
        || !core_version
            .split('.')
            .all(|segment| !segment.is_empty() && segment.bytes().all(|byte| byte.is_ascii_digit()))
    {
        return Err("当前内置内核版本无效，无法兼容旧版更新包".to_string());
    }
    let core_archive_name = format!("CLIProxyAPI_{core_version}_windows_{arch}.zip");
    let current_core_archive = app_dir.join("cpa-core").join(&core_archive_name);
    if !current_core_archive.is_file() {
        return Err(format!(
            "当前便携版缺少内置内核压缩包 {core_archive_name}，无法兼容旧版更新包"
        ));
    }
    let staged_core_dir = staging_dir.join("cpa-core");
    fs::create_dir_all(&staged_core_dir)
        .map_err(|error| format!("创建旧版更新兼容暂存目录失败: {error}"))?;
    fs::copy(&current_core_version, staging_dir.join(CORE_VERSION_FILE))
        .map_err(|error| format!("暂存当前内核版本文件失败: {error}"))?;
    fs::copy(
        current_core_archive,
        staged_core_dir.join(&core_archive_name),
    )
    .map_err(|error| format!("暂存当前内置内核压缩包失败: {error}"))?;
    Ok(core_archive_name)
}

#[cfg(windows)]
pub(crate) fn preflight_portable_update_directory(app_dir: &Path) -> Result<(), String> {
    if !app_dir.join(CORE_VERSION_FILE).is_file() || !app_dir.join("cpa-core").is_dir() {
        return Err("当前便携版目录不完整，请下载最新版完整包覆盖升级".to_string());
    }
    let probe = app_dir.join(format!(
        ".easycliproxy-update-write-test-{}",
        std::process::id()
    ));
    fs::write(&probe, b"update-write-test")
        .map_err(|error| format!("应用目录不可写，无法自动升级: {error}"))?;
    fs::remove_file(&probe).map_err(|error| format!("清理应用更新写入测试失败: {error}"))?;
    let core_probe = app_dir.join("cpa-core").join(format!(
        ".easycliproxy-update-write-test-{}",
        std::process::id()
    ));
    fs::write(&core_probe, b"update-write-test")
        .map_err(|error| format!("内置内核目录不可写，无法自动升级: {error}"))?;
    fs::remove_file(&core_probe)
        .map_err(|error| format!("清理内置内核更新写入测试失败: {error}"))?;
    Ok(())
}

#[cfg(windows)]
pub(crate) fn wait_for_windows_process_exit(
    process_id: u32,
    timeout: Duration,
) -> Result<(), String> {
    use windows_sys::Win32::{
        Foundation::{CloseHandle, WAIT_FAILED, WAIT_TIMEOUT},
        System::Threading::{OpenProcess, WaitForSingleObject},
    };

    const SYNCHRONIZE_ACCESS: u32 = 0x0010_0000;
    let handle = unsafe { OpenProcess(SYNCHRONIZE_ACCESS, 0, process_id) };
    if handle.is_null() {
        let error = io::Error::last_os_error();
        if error.raw_os_error() == Some(87) {
            return Ok(());
        }
        return Err(format!("打开旧版应用进程失败: {error}"));
    }
    let timeout_ms = timeout.as_millis().min(u32::MAX as u128) as u32;
    let result = unsafe { WaitForSingleObject(handle, timeout_ms) };
    unsafe { CloseHandle(handle) };
    if result == WAIT_TIMEOUT {
        return Err("等待旧版应用退出超时".to_string());
    }
    if result == WAIT_FAILED {
        return Err(format!(
            "等待旧版应用退出失败: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(())
}

#[cfg(windows)]
pub(crate) fn validate_portable_update_descriptor(
    descriptor_path: &Path,
    descriptor: &PortableUpdateDescriptor,
) -> Result<(), String> {
    if descriptor.parent_pid == 0
        || semver::Version::parse(descriptor.target_version.trim().trim_start_matches('v')).is_err()
    {
        return Err("应用更新描述无效".to_string());
    }
    let app_dir = descriptor
        .current_exe
        .parent()
        .ok_or_else(|| "应用更新目标路径无效".to_string())?;
    if !descriptor.current_exe.is_absolute()
        || descriptor.current_exe != app_dir.join(PORTABLE_APP_BINARY)
        || descriptor.current_manifest != app_dir.join(PORTABLE_APP_MANIFEST_FILE)
        || descriptor.backup_exe != app_dir.join(".EasyCLIProxyAPI.exe.update-backup")
        || descriptor.backup_manifest != app_dir.join(".portable-app.json.update-backup")
        || descriptor.current_core_version != app_dir.join(CORE_VERSION_FILE)
        || descriptor.backup_core_version != app_dir.join(".core-version.txt.update-backup")
    {
        return Err("应用更新目标路径无效".to_string());
    }
    let core_archive_name = descriptor
        .staged_core_archive
        .file_name()
        .ok_or_else(|| "应用更新内置内核文件名无效".to_string())?;
    if descriptor.target_core_archive != app_dir.join("cpa-core").join(core_archive_name)
        || !core_archive_name
            .to_string_lossy()
            .starts_with("CLIProxyAPI_")
        || !core_archive_name.to_string_lossy().ends_with(".zip")
        || descriptor.install_core_archive == descriptor.target_core_archive.is_file()
    {
        return Err("应用更新内置内核目标路径无效".to_string());
    }
    let canonical_work_dir = fs::canonicalize(&descriptor.work_dir)
        .map_err(|error| format!("读取应用更新临时目录失败: {error}"))?;
    let canonical_temp_dir = fs::canonicalize(env::temp_dir())
        .map_err(|error| format!("读取系统临时目录失败: {error}"))?;
    if !canonical_work_dir.starts_with(&canonical_temp_dir)
        || !canonical_work_dir
            .file_name()
            .and_then(|value| value.to_str())
            .is_some_and(|value| value.starts_with("EasyCLIProxyAPI-update-"))
    {
        return Err("应用更新工作目录无效".to_string());
    }
    let canonical_descriptor = fs::canonicalize(descriptor_path)
        .map_err(|error| format!("读取应用更新描述路径失败: {error}"))?;
    if canonical_descriptor.parent() != Some(canonical_work_dir.as_path())
        || canonical_descriptor
            .file_name()
            .and_then(|value| value.to_str())
            != Some("update-descriptor.json")
        || descriptor.ack_path != descriptor.work_dir.join("update-started.ack")
    {
        return Err("应用更新描述路径越界".to_string());
    }
    for staged in [
        &descriptor.staged_exe,
        &descriptor.staged_manifest,
        &descriptor.staged_core_version,
        &descriptor.staged_core_archive,
    ] {
        let canonical = fs::canonicalize(staged)
            .map_err(|error| format!("读取应用更新暂存文件失败: {error}"))?;
        if !canonical.starts_with(&canonical_work_dir) {
            return Err("应用更新暂存路径越界".to_string());
        }
    }
    if descriptor.staged_exe
        != descriptor
            .work_dir
            .join("staging")
            .join(PORTABLE_APP_BINARY)
        || descriptor.staged_manifest
            != descriptor
                .work_dir
                .join("staging")
                .join(PORTABLE_APP_MANIFEST_FILE)
        || descriptor.staged_core_version
            != descriptor.work_dir.join("staging").join(CORE_VERSION_FILE)
        || descriptor.staged_core_archive
            != descriptor
                .work_dir
                .join("staging")
                .join("cpa-core")
                .join(core_archive_name)
    {
        return Err("应用更新暂存路径无效".to_string());
    }
    Ok(())
}

#[cfg(windows)]
pub(crate) fn restore_portable_update_backup(
    descriptor: &PortableUpdateDescriptor,
) -> Result<(), String> {
    if !descriptor.backup_exe.is_file()
        || !descriptor.backup_manifest.is_file()
        || !descriptor.backup_core_version.is_file()
    {
        return Err("应用更新备份不完整，无法回滚".to_string());
    }
    if descriptor.current_exe.exists() {
        fs::remove_file(&descriptor.current_exe)
            .map_err(|error| format!("移除新版应用失败: {error}"))?;
    }
    fs::rename(&descriptor.backup_exe, &descriptor.current_exe)
        .map_err(|error| format!("恢复旧版应用失败: {error}"))?;
    if descriptor.current_manifest.exists() {
        fs::remove_file(&descriptor.current_manifest)
            .map_err(|error| format!("移除新版便携版标识失败: {error}"))?;
    }
    fs::rename(&descriptor.backup_manifest, &descriptor.current_manifest)
        .map_err(|error| format!("恢复旧版便携版标识失败: {error}"))?;
    if descriptor.current_core_version.exists() {
        fs::remove_file(&descriptor.current_core_version)
            .map_err(|error| format!("移除新版内核版本文件失败: {error}"))?;
    }
    fs::rename(
        &descriptor.backup_core_version,
        &descriptor.current_core_version,
    )
    .map_err(|error| format!("恢复旧版内核版本文件失败: {error}"))?;
    if descriptor.install_core_archive && descriptor.target_core_archive.exists() {
        fs::remove_file(&descriptor.target_core_archive)
            .map_err(|error| format!("移除新版内置内核压缩包失败: {error}"))?;
    }
    Ok(())
}

#[cfg(windows)]
pub(crate) fn replace_portable_update_files(
    descriptor: &PortableUpdateDescriptor,
) -> Result<(), String> {
    let app_dir = descriptor
        .current_exe
        .parent()
        .ok_or_else(|| "应用更新目标路径无效".to_string())?;
    let replacement_exe = app_dir.join(".EasyCLIProxyAPI.exe.update-new");
    let replacement_manifest = app_dir.join(".portable-app.json.update-new");
    let replacement_core_version = app_dir.join(".core-version.txt.update-new");
    let replacement_core_archive = app_dir.join(".cpa-core-archive.update-new");
    let _ = fs::remove_file(&replacement_exe);
    let _ = fs::remove_file(&replacement_manifest);
    let _ = fs::remove_file(&replacement_core_version);
    let _ = fs::remove_file(&replacement_core_archive);
    fs::copy(&descriptor.staged_exe, &replacement_exe)
        .map_err(|error| format!("准备新版应用程序失败: {error}"))?;
    if let Err(error) = fs::copy(&descriptor.staged_manifest, &replacement_manifest) {
        let _ = fs::remove_file(&replacement_exe);
        return Err(format!("准备新版便携版标识失败: {error}"));
    }
    if let Err(error) = fs::copy(&descriptor.staged_core_version, &replacement_core_version) {
        let _ = fs::remove_file(&replacement_exe);
        let _ = fs::remove_file(&replacement_manifest);
        return Err(format!("准备新版内核版本文件失败: {error}"));
    }
    if descriptor.install_core_archive {
        if let Err(error) = fs::copy(&descriptor.staged_core_archive, &replacement_core_archive) {
            let _ = fs::remove_file(&replacement_exe);
            let _ = fs::remove_file(&replacement_manifest);
            let _ = fs::remove_file(&replacement_core_version);
            return Err(format!("准备新版内置内核压缩包失败: {error}"));
        }
    }

    let _ = fs::remove_file(&descriptor.backup_exe);
    let _ = fs::remove_file(&descriptor.backup_manifest);
    let _ = fs::remove_file(&descriptor.backup_core_version);
    if let Err(error) = fs::rename(&descriptor.current_exe, &descriptor.backup_exe) {
        let _ = fs::remove_file(&replacement_exe);
        let _ = fs::remove_file(&replacement_manifest);
        let _ = fs::remove_file(&replacement_core_version);
        let _ = fs::remove_file(&replacement_core_archive);
        return Err(format!("备份旧版应用失败: {error}"));
    }
    if let Err(error) = fs::rename(&descriptor.current_manifest, &descriptor.backup_manifest) {
        let _ = fs::rename(&descriptor.backup_exe, &descriptor.current_exe);
        let _ = fs::remove_file(&replacement_exe);
        let _ = fs::remove_file(&replacement_manifest);
        let _ = fs::remove_file(&replacement_core_version);
        let _ = fs::remove_file(&replacement_core_archive);
        return Err(format!("备份便携版标识失败: {error}"));
    }
    if let Err(error) = fs::rename(
        &descriptor.current_core_version,
        &descriptor.backup_core_version,
    ) {
        let _ = fs::rename(&descriptor.backup_manifest, &descriptor.current_manifest);
        let _ = fs::rename(&descriptor.backup_exe, &descriptor.current_exe);
        let _ = fs::remove_file(&replacement_exe);
        let _ = fs::remove_file(&replacement_manifest);
        let _ = fs::remove_file(&replacement_core_version);
        let _ = fs::remove_file(&replacement_core_archive);
        return Err(format!("备份旧版内核版本文件失败: {error}"));
    }

    let replace_result = (|| -> Result<(), String> {
        fs::rename(&replacement_exe, &descriptor.current_exe)
            .map_err(|error| format!("替换应用程序失败: {error}"))?;
        fs::rename(&replacement_manifest, &descriptor.current_manifest)
            .map_err(|error| format!("替换便携版标识失败: {error}"))?;
        fs::rename(&replacement_core_version, &descriptor.current_core_version)
            .map_err(|error| format!("替换内核版本文件失败: {error}"))?;
        if descriptor.install_core_archive {
            fs::rename(&replacement_core_archive, &descriptor.target_core_archive)
                .map_err(|error| format!("安装新版内置内核压缩包失败: {error}"))?;
        }
        Ok(())
    })();
    if let Err(error) = replace_result {
        let _ = fs::remove_file(&replacement_exe);
        let _ = fs::remove_file(&replacement_manifest);
        let _ = fs::remove_file(&replacement_core_version);
        let _ = fs::remove_file(&replacement_core_archive);
        restore_portable_update_backup(descriptor)
            .map_err(|rollback| format!("{error}；{rollback}"))?;
        return Err(error);
    }
    Ok(())
}

#[cfg(windows)]
pub(crate) fn cleanup_superseded_bundled_core_archives(descriptor: &PortableUpdateDescriptor) {
    let Some(core_dir) = descriptor.target_core_archive.parent() else {
        return;
    };
    let Some(current_name) = descriptor
        .target_core_archive
        .file_name()
        .and_then(|value| value.to_str())
    else {
        return;
    };
    let Ok(entries) = fs::read_dir(core_dir) else {
        return;
    };
    for entry in entries.filter_map(Result::ok) {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().into_owned();
        if path.is_file()
            && name != current_name
            && name.starts_with("CLIProxyAPI_")
            && name.ends_with(".zip")
            && !name.contains("_no-plugin")
        {
            if let Err(error) = fs::remove_file(&path) {
                eprintln!(
                    "清理旧版内置内核压缩包失败 {}: {error}",
                    path_to_string(&path)
                );
            }
        }
    }
}

#[cfg(windows)]
pub(crate) fn cleanup_portable_update_payload(
    descriptor_path: &Path,
    descriptor: &PortableUpdateDescriptor,
) {
    let _ = fs::remove_file(descriptor.work_dir.join("update.zip"));
    let _ = fs::remove_dir_all(descriptor.work_dir.join("staging"));
    let _ = fs::remove_file(&descriptor.ack_path);
    let _ = fs::remove_file(descriptor_path);
}

#[cfg(windows)]
pub(crate) fn run_portable_update_helper(descriptor_path: &Path) -> Result<(), String> {
    let descriptor = serde_json::from_slice::<PortableUpdateDescriptor>(
        &fs::read(descriptor_path).map_err(|error| format!("读取应用更新描述失败: {error}"))?,
    )
    .map_err(|error| format!("解析应用更新描述失败: {error}"))?;
    validate_portable_update_descriptor(descriptor_path, &descriptor)?;
    wait_for_windows_process_exit(descriptor.parent_pid, Duration::from_secs(120))?;

    if let Err(error) = replace_portable_update_files(&descriptor) {
        let mut rollback = Command::new(&descriptor.current_exe);
        configure_background_command(&mut rollback);
        let _ = rollback.spawn();
        cleanup_portable_update_payload(descriptor_path, &descriptor);
        return Err(error);
    }

    let _ = fs::remove_file(&descriptor.ack_path);
    let mut command = Command::new(&descriptor.current_exe);
    command
        .arg("--portable-update-ack")
        .arg(&descriptor.ack_path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    configure_background_command(&mut command);
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            restore_portable_update_backup(&descriptor)?;
            let mut rollback = Command::new(&descriptor.current_exe);
            configure_background_command(&mut rollback);
            let _ = rollback.spawn();
            cleanup_portable_update_payload(descriptor_path, &descriptor);
            return Err(format!("启动新版应用失败: {error}"));
        }
    };

    let deadline = Instant::now() + Duration::from_secs(60);
    let mut confirmed = false;
    while Instant::now() < deadline {
        if descriptor.ack_path.is_file() {
            confirmed = true;
            break;
        }
        if child
            .try_wait()
            .map_err(|error| format!("检查新版应用状态失败: {error}"))?
            .is_some()
        {
            break;
        }
        thread::sleep(Duration::from_millis(200));
    }

    if confirmed {
        let _ = fs::remove_file(&descriptor.backup_exe);
        let _ = fs::remove_file(&descriptor.backup_manifest);
        let _ = fs::remove_file(&descriptor.backup_core_version);
        cleanup_superseded_bundled_core_archives(&descriptor);
        cleanup_portable_update_payload(descriptor_path, &descriptor);
        return Ok(());
    }

    let _ = child.kill();
    let _ = child.wait();
    restore_portable_update_backup(&descriptor)?;
    let mut rollback = Command::new(&descriptor.current_exe);
    configure_background_command(&mut rollback);
    let _ = rollback.spawn();
    cleanup_portable_update_payload(descriptor_path, &descriptor);
    Err(format!(
        "新版应用 {} 未能在 60 秒内完成启动确认，已回滚",
        descriptor.target_version
    ))
}

pub(crate) fn portable_update_ack_argument() -> Option<PathBuf> {
    let mut args = env::args_os();
    while let Some(argument) = args.next() {
        if argument == "--portable-update-ack" {
            let path = PathBuf::from(args.next()?);
            let parent = path.parent()?;
            let valid_name =
                path.file_name().and_then(|value| value.to_str()) == Some("update-started.ack");
            let valid_parent = parent.starts_with(env::temp_dir())
                && parent
                    .file_name()
                    .and_then(|value| value.to_str())
                    .is_some_and(|value| value.starts_with("EasyCLIProxyAPI-update-"));
            return (valid_name && valid_parent).then_some(path);
        }
    }
    None
}
