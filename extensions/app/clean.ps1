[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RootDir = [System.IO.Path]::GetFullPath($PSScriptRoot).TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
)
$RootPrefix = "$RootDir$([System.IO.Path]::DirectorySeparatorChar)"
$BinDir = Join-Path $RootDir 'bin-work'

Set-Location -LiteralPath $RootDir

# bin-work is also the working folder of the app started by run.ps1: it holds that app's
# config.toml, oauth credentials, databases, logs and installed cores. Remove only the
# files build.ps1 and copy.ps1 (scripts/portable.mjs) put there.
$CleanTargets = @(
    (Join-Path $RootDir 'dist'),
    (Join-Path $RootDir 'src-tauri\target'),
    (Join-Path $RootDir 'src-tauri\gen')
)
foreach ($Name in @(
    'EasyCLIProxyAPI',
    'EasyCLIProxyAPI.exe',
    'cpa-gui',
    'cpa-gui.exe',
    'core-version.txt',
    'portable-app.json'
)) {
    $CleanTargets += Join-Path $BinDir $Name
}
$CoreDir = Join-Path $BinDir 'cpa-core'
if (Test-Path -LiteralPath $CoreDir -PathType Container) {
    $CleanTargets += @(
        Get-ChildItem -LiteralPath $CoreDir -File |
            Where-Object { $_.Name -like 'CLIProxyAPI_*.zip' -or $_.Name -like 'CLIProxyAPI_*.tar.gz' } |
            ForEach-Object { $_.FullName }
    )
}

foreach ($Target in $CleanTargets) {
    $ResolvedTarget = [System.IO.Path]::GetFullPath($Target)
    if (-not $ResolvedTarget.StartsWith(
        $RootPrefix,
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
        throw "Refusing to clean a path outside the project: $ResolvedTarget"
    }

    if (Test-Path -LiteralPath $ResolvedTarget) {
        Remove-Item -LiteralPath $ResolvedTarget -Recurse -Force
    }
}

Write-Host 'Cleaned build output. Kept app data in bin-work (config, credentials, databases, logs).'
