use crate::{
    CoreApiKeyView, CoreConfigSettings, CoreConfigView, CoreConnectionMode, CoreStatus,
    GuiConfigFile, GuiConfigState,
};
use serde::{Deserialize, Serialize};
use std::time::{Duration, Instant};
use tauri::Manager;

#[derive(Clone, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CoreConnection {
    pub(crate) mode: CoreConnectionMode,
    pub(crate) management_origin: String,
    pub(crate) management_key: String,
}

impl From<&GuiConfigFile> for CoreConnection {
    fn from(config: &GuiConfigFile) -> Self {
        Self {
            mode: config.connection_mode,
            management_origin: config.remote_management_origin.clone(),
            management_key: config.remote_management_key.clone(),
        }
    }
}

pub(crate) fn normalize_origin(value: &str) -> Result<String, String> {
    let value = value.trim();
    let parsed = reqwest::Url::parse(value).map_err(|_| "Invalid management origin")?;
    if value.chars().any(char::is_control)
        || value.contains(['\\', '@'])
        || value.split_once("://").is_none_or(|(_, authority)| {
            authority
                .split_once('/')
                .is_some_and(|(_, path)| !path.is_empty())
        })
        || !matches!(parsed.scheme(), "http" | "https")
        || parsed.host_str().is_none()
        || !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
        || (parsed.path() != "/" && !parsed.path().is_empty())
    {
        return Err("Management origin must be an HTTP(S) origin without credentials, path, query or fragment".into());
    }
    Ok(parsed.to_string().trim_end_matches('/').to_string())
}

pub(crate) fn normalize_connection(mut connection: CoreConnection) -> Result<CoreConnection, String> {
    connection.management_origin = normalize_origin(&connection.management_origin)?;
    connection.management_key = crate::normalize_management_secret_key(connection.management_key)?;
    Ok(connection)
}

pub(crate) fn require_local(config: &GuiConfigFile) -> Result<(), String> {
    if config.connection_mode == CoreConnectionMode::Remote {
        Err("This operation is only available for the local core".into())
    } else {
        Ok(())
    }
}

/// The remote core's configuration, read through the management API. `None` in Local mode, where callers
/// read the local config.yaml as before. Remote values are shown read-only; nothing is written back.
pub(crate) async fn remote_core_config_document(
    config: &GuiConfigFile,
) -> Result<Option<serde_norway::Value>, String> {
    if config.connection_mode != CoreConnectionMode::Remote {
        return Ok(None);
    }
    // One short-lived snapshot for simultaneous settings getters and model probes. Holding the
    // async lock across the read coalesces in-flight requests; credentials are part of the target.
    // Cache failures too, so a failing target is not read again by every queued caller.
    type Snapshot = (CoreConnection, Instant, Result<serde_norway::Value, String>);
    static CACHE: tokio::sync::Mutex<Option<Snapshot>> = tokio::sync::Mutex::const_new(None);
    let target = CoreConnection::from(config);
    let mut cache = CACHE.lock().await;
    if let Some((cached_target, fetched_at, document)) = cache.as_ref() {
        if *cached_target == target && fetched_at.elapsed() < Duration::from_secs(5) {
            return document.clone().map(Some);
        }
    }
    let document = match crate::core_config::fetch_management_raw_config_yaml(config).await {
        Ok(content) => serde_norway::from_str(&content)
            .map_err(|error| format!("Failed to parse the remote core configuration: {error}")),
        Err(error) => Err(error),
    };
    *cache = Some((target, Instant::now(), document.clone()));
    document.map(Some)
}

/// Settings view of a remote core. Key remarks are GUI metadata of this computer, so none are shown.
pub(crate) fn remote_config_view(settings: CoreConfigSettings) -> CoreConfigView {
    CoreConfigView {
        allow_lan: !crate::core_config::is_loopback_host(&settings.host),
        host: settings.host,
        api_keys: settings
            .api_keys
            .into_iter()
            .map(|api_key| CoreApiKeyView {
                api_key,
                remark: String::new(),
            })
            .collect(),
        management_secret_configured: settings.management_secret_configured,
        port: settings.port,
        debug: settings.debug,
        commercial_mode: settings.commercial_mode,
        logging_to_file: settings.logging_to_file,
        logs_max_total_size_mb: settings.logs_max_total_size_mb,
        error_logs_max_files: settings.error_logs_max_files,
        usage_statistics_enabled: settings.usage_statistics_enabled,
        redis_usage_queue_retention_seconds: settings.redis_usage_queue_retention_seconds,
        request_log: settings.request_log,
        plugins_enabled: settings.plugins_enabled,
        routing_strategy: settings.routing_strategy,
        proxy_override: !settings.proxy_url.is_empty(),
        proxy_url: settings.proxy_url,
        routing_session_affinity: settings.routing_session_affinity,
        routing_session_affinity_ttl: settings.routing_session_affinity_ttl,
        disable_cooling: settings.disable_cooling,
        request_retry: settings.request_retry,
        max_retry_credentials: settings.max_retry_credentials,
        max_retry_interval: settings.max_retry_interval,
        streaming_bootstrap_retries: settings.streaming_bootstrap_retries,
    }
}

#[tauri::command]
pub(crate) fn get_core_connection(
    gui_config_state: tauri::State<'_, GuiConfigState>,
) -> Result<CoreConnection, String> {
    Ok(CoreConnection::from(&gui_config_state.snapshot()?))
}

#[tauri::command]
pub(crate) fn save_core_connection(
    app: tauri::AppHandle,
    gui_config_state: tauri::State<'_, GuiConfigState>,
    connection: CoreConnection,
) -> Result<CoreConnection, String> {
    let connection = if connection.mode == CoreConnectionMode::Remote {
        normalize_connection(connection)?
    } else {
        connection
    };
    let process = app.state::<crate::CoreProcessState>();
    let _operation = crate::lock_core_operation(process.inner())?;
    if connection.mode == CoreConnectionMode::Remote {
        // Includes adopted processes, matching the managed flag the UI checks.
        if process.managed_pid().is_some() {
            return Err("Stop the GUI-managed local core before switching to Remote".into());
        }
        process.recovery.cancel();
    }
    let config = gui_config_state.update(|config| {
        config.connection_mode = connection.mode;
        if connection.mode == CoreConnectionMode::Remote {
            config.remote_management_origin = connection.management_origin;
            config.remote_management_key = connection.management_key;
        }
        Ok(())
    })?;
    if config.connection_mode == CoreConnectionMode::Remote {
        crate::usage::stop_usage_collector(&app);
    } else {
        crate::usage::start_usage_collector(app);
    }
    Ok(CoreConnection::from(&config))
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct CoreConnectionTestResult {
    pub(crate) connected: bool,
    pub(crate) version: Option<String>,
    pub(crate) message: String,
}

#[tauri::command]
pub(crate) async fn test_core_connection(
    connection: CoreConnection,
) -> Result<CoreConnectionTestResult, String> {
    check_remote_connection(connection, true).await
}

pub(crate) async fn poll_core_connection(
    connection: CoreConnection,
) -> Result<CoreConnectionTestResult, String> {
    check_remote_connection(connection, false).await
}

async fn check_remote_connection(
    connection: CoreConnection,
    full_config: bool,
) -> Result<CoreConnectionTestResult, String> {
    let connection = normalize_connection(connection)?;
    let config = GuiConfigFile {
        connection_mode: CoreConnectionMode::Remote,
        remote_management_origin: connection.management_origin,
        remote_management_key: connection.management_key,
        ..GuiConfigFile::default()
    };
    let response = crate::management_api::management_http_client_for(&config)?
        .get(crate::management_api::management_endpoint(
            &config,
            if full_config { "config" } else { "config/config-version" },
        )?)
        .header(
            "Authorization",
            crate::management_api::management_authorization(&config)?,
        )
        .timeout(std::time::Duration::from_secs(10))
        .send()
        .await;
    let mut result = CoreConnectionTestResult {
        connected: false,
        version: None,
        message: "Unable to reach the core".into(),
    };
    if let Ok(response) = response {
        let status = response.status();
        if status.is_success() {
            let version = response
                .headers()
                .get("X-CPA-VERSION")
                .and_then(|value| value.to_str().ok())
                .map(str::to_string);
            let valid = response
                .json::<serde_json::Value>()
                .await
                .ok()
                .and_then(|value| {
                    if full_config {
                        value.get("config-version")?.as_u64()
                    } else {
                        value.as_u64()
                    }
                })
                == Some(8);
            result.connected = valid;
            result.version = if valid { version } else { None };
            result.message = if valid {
                "Connected"
            } else {
                "Core did not return a v8 configuration"
            }
            .into();
        } else {
            result.message = match status.as_u16() {
                401 => "Core rejected the management key".into(),
                403 => "Remote management access is forbidden".into(),
                404 => "Core does not expose the v8 management API".into(),
                status => format!("Core management request failed (HTTP {status})"),
            };
        }
    }
    Ok(result)
}

pub(crate) fn remote_status(
    connection: &CoreConnection,
    result: CoreConnectionTestResult,
) -> CoreStatus {
    CoreStatus {
        installed: false,
        running: result.connected,
        ready: result.connected,
        starting: false,
        managed: false,
        process_id: None,
        current_version: result.version,
        install_dir: String::new(),
        binary_path: None,
        connection_mode: CoreConnectionMode::Remote,
        remote_origin: Some(connection.management_origin.clone()),
        message: result.message,
        recovery: None,
    }
}
