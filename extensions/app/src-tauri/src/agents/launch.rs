use super::*;

#[tauri::command]
pub(crate) fn launch_agent(
    app: tauri::AppHandle,
    gui_config_state: tauri::State<'_, GuiConfigState>,
    client: String,
    target: Option<String>,
    working_directory: Option<String>,
) -> Result<(), String> {
    let home = app
        .path()
        .home_dir()
        .map_err(|error| format!("无法获取用户目录: {error}"))?;
    let config = gui_config_state.snapshot()?;
    let requested_target = target
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty());

    if client.trim().eq_ignore_ascii_case(PI_AGENT_ID) {
        if requested_target.is_some_and(|value| value != "cli") {
            return Err("Pi 只支持 CLI 启动方式".to_string());
        }
        let status =
            inspect_pi_provider_status(&home, config.port, effective_agent_api_key(&config));
        if !status.installed {
            return Err("未检测到 Pi CLI，请先安装 Pi 并重新检测".to_string());
        }
        let executable =
            find_pi_executable(&home).ok_or_else(|| "未找到 Pi CLI 可执行文件".to_string())?;
        let launch_directory = resolve_launch_directory(working_directory.as_deref(), &home)?;
        return launch_cli_agent(&executable, PI_AGENT_NAME, &launch_directory, &[]);
    }

    let client = AgentClient::parse(&client)?;
    if !client.supported_platform() {
        return Err(format!("当前平台不支持启动 {}", client.name()));
    }
    let status = inspect_agent_config(client, &home, config.port, effective_agent_api_key(&config));
    if !status.installed {
        return Err(format!("未检测到 {}，请先安装并重新检测", client.name()));
    }
    let default_target = match client {
        AgentClient::ClaudeDesktop | AgentClient::ZCode => "app",
        AgentClient::Codex if find_codex_app_installation(&home).is_some() => "app",
        _ => "cli",
    };
    let requested_target = requested_target.unwrap_or(default_target);
    match (client, requested_target) {
        (AgentClient::ClaudeDesktop, "app") => launch_claude_desktop(&home),
        (AgentClient::ZCode, "app") => {
            let executable = find_zcode_desktop_executable(&home)
                .ok_or_else(|| "未找到 ZCode 应用程序".to_string())?;
            launch_desktop_agent(&executable, client.name())
        }
        (AgentClient::Codex, "app") => launch_codex_desktop(&home),
        (AgentClient::ClaudeDesktop | AgentClient::ZCode, "cli") => {
            Err(format!("{} 不支持 CLI 启动方式", client.name()))
        }
        (_, "cli") => {
            let executable = find_agent_executable(client, &home)
                .ok_or_else(|| format!("未找到 {} 的可执行文件", client.name()))?;
            let launch_directory = resolve_launch_directory(working_directory.as_deref(), &home)?;
            let environment_to_remove = if client == AgentClient::ClaudeCode {
                &["ANTHROPIC_API_KEY"][..]
            } else {
                &[]
            };
            launch_cli_agent(
                &executable,
                client.name(),
                &launch_directory,
                environment_to_remove,
            )
        }
        (_, "app") => Err(format!("{} 不支持桌面 App 启动方式", client.name())),
        _ => Err("不支持的智能体启动方式".to_string()),
    }
}

#[tauri::command]
pub(crate) async fn restart_codex_app(app: tauri::AppHandle) -> Result<(), String> {
    let home = app
        .path()
        .home_dir()
        .map_err(|error| format!("无法获取用户目录: {error}"))?;
    let target = find_codex_app_installation(&home)
        .ok_or_else(|| "未检测到 Codex 桌面应用，请重新检测或改用 Codex CLI".to_string())?;

    tauri::async_runtime::spawn_blocking(move || {
        stop_codex_desktop(&target)?;
        launch_codex_target(&target)
    })
    .await
    .map_err(|error| format!("重启 Codex App 任务失败: {error}"))?
}

fn resolve_launch_directory(value: Option<&str>, fallback: &Path) -> Result<PathBuf, String> {
    let Some(value) = value.map(str::trim).filter(|value| !value.is_empty()) else {
        return Ok(fallback.to_path_buf());
    };
    if value.chars().any(char::is_control) {
        return Err("工作目录包含无效字符".to_string());
    }
    let path = PathBuf::from(value);
    if !path.is_absolute() {
        return Err("工作目录必须是绝对路径".to_string());
    }
    if !path.is_dir() {
        return Err(format!("工作目录不存在: {}", path_to_string(&path)));
    }
    Ok(path)
}

fn launch_codex_desktop(home: &Path) -> Result<(), String> {
    let target = find_codex_app_installation(home)
        .ok_or_else(|| "未检测到 Codex 桌面应用，请重新检测或改用 Codex CLI".to_string())?;
    launch_codex_target(&target)
}

fn launch_codex_target(target: &CodexAppTarget) -> Result<(), String> {
    match target {
        #[cfg(target_os = "windows")]
        CodexAppTarget::WindowsAppId(app_id) => launch_windows_store_app(app_id, "Codex App"),
        CodexAppTarget::Application(path) => launch_desktop_agent(path, "Codex App"),
    }
}

#[cfg(target_os = "windows")]
fn stop_codex_desktop(target: &CodexAppTarget) -> Result<(), String> {
    let script = windows_codex_stop_script(target);
    let encoded = windows_powershell_encoded_command(&script);
    let mut command = Command::new(windows_powershell_executable());
    command.args([
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy",
        "Bypass",
        "-EncodedCommand",
        &encoded,
    ]);
    configure_background_command(&mut command);
    let output = command
        .output()
        .map_err(|error| format!("关闭 Codex App 失败: {error}"))?;
    if output.status.success() {
        return Ok(());
    }
    let detail = String::from_utf8_lossy(if output.stderr.is_empty() {
        &output.stdout
    } else {
        &output.stderr
    })
    .trim()
    .to_string();
    Err(if detail.is_empty() {
        "Codex App 未能完全关闭".to_string()
    } else {
        format!("关闭 Codex App 失败: {detail}")
    })
}

#[cfg(target_os = "windows")]
fn windows_codex_stop_script(target: &CodexAppTarget) -> String {
    let selector = match target {
        CodexAppTarget::Application(path) => format!(
            "$targetExecutable = {}\n$targetRoot = $null",
            windows_powershell_single_quoted_literal(&path_to_string(path))
        ),
        CodexAppTarget::WindowsAppId(app_id) => {
            let package_family = app_id.split('!').next().unwrap_or(app_id);
            format!(
                concat!(
                    "$targetExecutable = $null\n",
                    "$packageFamily = {}\n",
                    "$package = @(Get-AppxPackage | Where-Object {{ $_.PackageFamilyName -eq $packageFamily }}) | Select-Object -First 1\n",
                    "$targetRoot = if ($package) {{ $package.InstallLocation }} else {{ $null }}\n",
                    "if (-not $targetRoot) {{ throw 'Codex App package directory was not found' }}"
                ),
                windows_powershell_single_quoted_literal(package_family)
            )
        }
    };
    format!(
        r#"$ErrorActionPreference = 'Stop'
{selector}
function Get-CodexAppProcesses {{
    @(Get-CimInstance Win32_Process | Where-Object {{
        $_.Name -in @('ChatGPT.exe', 'Codex.exe') -and
        $_.ExecutablePath -and
        (($targetExecutable -and [string]::Equals($_.ExecutablePath, $targetExecutable, [System.StringComparison]::OrdinalIgnoreCase)) -or
         ($targetRoot -and $_.ExecutablePath.StartsWith(($targetRoot.TrimEnd('\') + '\'), [System.StringComparison]::OrdinalIgnoreCase)))
    }})
}}
$processes = @(Get-CodexAppProcesses)
foreach ($process in $processes) {{
    Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
}}
$deadline = [DateTime]::UtcNow.AddSeconds(5)
do {{
    $remaining = @(Get-CodexAppProcesses)
    if ($remaining.Count -eq 0) {{ exit 0 }}
    Start-Sleep -Milliseconds 100
}} while ([DateTime]::UtcNow -lt $deadline)
throw "Codex App did not exit; remaining process IDs: $($remaining.ProcessId -join ', ')"
"#
    )
}

#[cfg(target_os = "macos")]
fn stop_codex_desktop(target: &CodexAppTarget) -> Result<(), String> {
    let CodexAppTarget::Application(executable) = target;
    let process_name = executable
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty())
        .ok_or_else(|| "无法识别 Codex App 进程名称".to_string())?;
    let application_name = executable
        .ancestors()
        .find(|path| path.extension().and_then(|value| value.to_str()) == Some("app"))
        .and_then(Path::file_stem)
        .and_then(|name| name.to_str())
        .unwrap_or(process_name);
    let escaped_name = application_name.replace('\\', "\\\\").replace('"', "\\\"");
    let _ = Command::new("osascript")
        .args([
            "-e",
            &format!("tell application \"{escaped_name}\" to quit"),
        ])
        .status();
    wait_for_unix_process_exit(process_name)
}

#[cfg(target_os = "macos")]
fn wait_for_unix_process_exit(process_name: &str) -> Result<(), String> {
    for _ in 0..50 {
        let status = Command::new("pgrep").args(["-x", process_name]).status();
        if status.is_ok_and(|status| !status.success()) {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(100));
    }
    let _ = Command::new("pkill")
        .args(["-KILL", "-x", process_name])
        .status();
    thread::sleep(Duration::from_millis(100));
    let status = Command::new("pgrep")
        .args(["-x", process_name])
        .status()
        .map_err(|error| format!("检查 Codex App 进程失败: {error}"))?;
    if status.success() {
        Err("Codex App 未能完全关闭".to_string())
    } else {
        Ok(())
    }
}

#[cfg(not(any(target_os = "windows", target_os = "macos")))]
fn stop_codex_desktop(_target: &CodexAppTarget) -> Result<(), String> {
    Err("当前平台不支持重启 Codex App".to_string())
}

fn launch_claude_desktop(home: &Path) -> Result<(), String> {
    if let Some(executable) = find_claude_desktop_executable(home) {
        return launch_desktop_agent(&executable, "Claude Desktop");
    }
    #[cfg(target_os = "windows")]
    {
        launch_windows_claude_store_app()
    }
    #[cfg(not(target_os = "windows"))]
    Err("未检测到 Claude Desktop 应用，请先安装或重新检测".to_string())
}

fn launch_desktop_agent(executable: &Path, label: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    let mut command = {
        let application = executable
            .ancestors()
            .find(|path| path.extension().and_then(|value| value.to_str()) == Some("app"))
            .unwrap_or(executable);
        let mut command = Command::new("open");
        command.arg(application);
        command
    };

    #[cfg(any(target_os = "windows", target_os = "linux"))]
    let mut command = Command::new(executable);

    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = (executable, label);
        return Err("当前平台不支持桌面智能体".to_string());
    }

    #[cfg(any(target_os = "macos", target_os = "windows", target_os = "linux"))]
    {
        command
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        configure_background_command(&mut command);
        command
            .spawn()
            .map(|_| ())
            .map_err(|error| format!("启动 {label} 失败: {error}"))
    }
}

#[cfg(target_os = "windows")]
fn launch_windows_store_app(app_id: &str, label: &str) -> Result<(), String> {
    let mut command = Command::new(windows_explorer_executable());
    command
        .arg(format!("shell:AppsFolder\\{app_id}"))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    configure_background_command(&mut command);
    command
        .spawn()
        .map(|_| ())
        .map_err(|error| format!("启动 {label} 失败: {error}"))
}

#[cfg(target_os = "windows")]
fn launch_windows_claude_store_app() -> Result<(), String> {
    const SCRIPT: &str = r#"
$ErrorActionPreference = 'Stop'
$appId = @(Get-StartApps) |
    Where-Object {
        $_.AppID -like 'Claude_*!*' -or
        $_.AppID -like 'Anthropic.Claude_*!*'
    } |
    Select-Object -First 1 -ExpandProperty AppID
if (-not $appId) { throw 'Claude Desktop app entry was not found' }
Start-Process "shell:AppsFolder\$appId"
"#;
    let encoded = windows_powershell_encoded_command(SCRIPT);
    let mut command = Command::new(windows_powershell_executable());
    command.args([
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy",
        "Bypass",
        "-EncodedCommand",
        &encoded,
    ]);
    configure_background_command(&mut command);
    let output = command
        .output()
        .map_err(|error| format!("启动 Claude Desktop 失败: {error}"))?;
    if output.status.success() {
        Ok(())
    } else {
        let detail = String::from_utf8_lossy(&output.stderr).trim().to_string();
        Err(if detail.is_empty() {
            "未找到可启动的 Claude Desktop 应用".to_string()
        } else {
            format!("启动 Claude Desktop 失败: {detail}")
        })
    }
}

#[cfg(target_os = "macos")]
fn shell_single_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

#[cfg(target_os = "macos")]
fn launch_cli_agent(
    executable: &Path,
    label: &str,
    working_directory: &Path,
    environment_to_remove: &[&str],
) -> Result<(), String> {
    let removals = environment_to_remove
        .iter()
        .map(|key| format!("-u {}", shell_single_quote(key)))
        .collect::<Vec<_>>()
        .join(" ");
    let command_line = format!(
        "cd {} && exec env {} {}",
        shell_single_quote(&path_to_string(working_directory)),
        removals,
        shell_single_quote(&path_to_string(executable)),
    );
    let script = format!(
        "tell application \"Terminal\"\nactivate\ndo script \"{}\"\nend tell",
        command_line.replace('\\', "\\\\").replace('"', "\\\"")
    );
    let output = Command::new("osascript")
        .arg("-e")
        .arg(script)
        .output()
        .map_err(|error| format!("启动 {label} 终端失败: {error}"))?;
    if output.status.success() {
        Ok(())
    } else {
        let detail = String::from_utf8_lossy(&output.stderr).trim().to_string();
        Err(format!("启动 {label} 终端失败: {detail}"))
    }
}

#[cfg(target_os = "linux")]
fn launch_cli_agent(
    executable: &Path,
    label: &str,
    working_directory: &Path,
    environment_to_remove: &[&str],
) -> Result<(), String> {
    let terminals: &[(&str, &[&str])] = &[
        ("x-terminal-emulator", &["-e"]),
        ("gnome-terminal", &["--"]),
        ("konsole", &["-e"]),
        ("xfce4-terminal", &["-e"]),
        ("mate-terminal", &["--"]),
        ("kitty", &["-e"]),
        ("alacritty", &["-e"]),
        ("ghostty", &["-e"]),
        ("xterm", &["-e"]),
    ];
    let path = env::var_os("PATH").unwrap_or_default();
    let mut last_error = None;
    for (terminal, arguments) in terminals {
        if !env::split_paths(&path).any(|directory| directory.join(terminal).is_file()) {
            continue;
        }
        let mut command = Command::new(terminal);
        command
            .args(*arguments)
            .arg(executable)
            .current_dir(working_directory)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        for key in environment_to_remove {
            command.env_remove(key);
        }
        match command.spawn() {
            Ok(_) => return Ok(()),
            Err(error) => last_error = Some(error.to_string()),
        }
    }
    Err(match last_error {
        Some(error) => format!("启动 {label} 失败: {error}"),
        None => format!("启动 {label} 失败：未找到可用的终端程序"),
    })
}

#[cfg(target_os = "windows")]
fn launch_cli_agent(
    executable: &Path,
    label: &str,
    working_directory: &Path,
    environment_to_remove: &[&str],
) -> Result<(), String> {
    use std::os::windows::process::CommandExt;

    const CREATE_NEW_CONSOLE: u32 = 0x0000_0010;
    let mut command = windows_command_for_executable(executable, true);
    command
        .current_dir(working_directory)
        .creation_flags(CREATE_NEW_CONSOLE);
    for key in environment_to_remove {
        command.env_remove(key);
    }
    command
        .spawn()
        .map(|_| ())
        .map_err(|error| format!("启动 {label} 失败: {error}"))
}

#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
fn launch_cli_agent(
    _executable: &Path,
    label: &str,
    _working_directory: &Path,
    _environment_to_remove: &[&str],
) -> Result<(), String> {
    Err(format!("当前平台不支持启动 {label}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn relative_launch_directory_is_rejected() {
        let error = resolve_launch_directory(Some("relative/project"), Path::new("/fallback"))
            .expect_err("relative path should be rejected");
        assert!(error.contains("绝对路径"));
    }

    #[test]
    fn omitted_launch_directory_uses_fallback() {
        let fallback = if cfg!(target_os = "windows") {
            Path::new(r"C:\Users\tester")
        } else {
            Path::new("/home/tester")
        };
        assert_eq!(resolve_launch_directory(None, fallback).unwrap(), fallback);
    }

    #[test]
    fn codex_exposes_independent_app_and_cli_targets() {
        let executable = Path::new("codex");
        let targets =
            agent_launch_targets(AgentClient::Codex, Some(executable), Some("1.0.0"), true);
        assert_eq!(
            targets
                .iter()
                .map(|target| target.id.as_str())
                .collect::<Vec<_>>(),
            ["app", "cli"]
        );
    }

    #[test]
    fn desktop_and_cli_clients_get_the_correct_target_kind() {
        let executable = Path::new("agent");
        let zcode =
            agent_launch_targets(AgentClient::ZCode, Some(executable), Some("1.0.0"), false);
        let kimi = agent_launch_targets(AgentClient::KimiCode, Some(executable), None, false);
        assert_eq!(zcode[0].id, "app");
        assert_eq!(kimi[0].id, "cli");
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn codex_restart_script_is_scoped_to_the_detected_installation() {
        let executable = CodexAppTarget::Application(PathBuf::from(r"C:\Apps\Codex\Codex.exe"));
        let executable_script = windows_codex_stop_script(&executable);
        assert!(executable_script.contains(r"C:\Apps\Codex\Codex.exe"));
        assert!(executable_script.contains("Get-CimInstance Win32_Process"));
        assert!(!executable_script.contains("taskkill"));

        let store = CodexAppTarget::WindowsAppId("OpenAI.Codex_123!App".to_string());
        let store_script = windows_codex_stop_script(&store);
        assert!(store_script.contains("OpenAI.Codex_123"));
        assert!(store_script.contains("PackageFamilyName"));
    }
}
