<#
.SYNOPSIS
Starts a Windows Runner using the current user's installed tools and login state.
.EXAMPLE
.\scripts\start-local-runner.ps1 -BindCode <code> -CliDirectory C:\Tools\cli
.EXAMPLE
.\scripts\start-local-runner.ps1 -Stop
#>
[CmdletBinding()]
param(
    [string]$BindCode,
    [string]$ServerUrl,
    [string]$WorkspaceRoot,
    [string]$RunnerBinary,
    [string]$GateBinary,
    [string]$CliDirectory,
    [string[]]$PathEntries,
    [string]$Name,
    [ValidateRange(1, 32)][int]$Concurrency = 1,
    [switch]$Stop
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runnerRoot = Join-Path $projectRoot '.local/windows/runner'
$configPath = Join-Path $runnerRoot 'config.json'
$pidPath = Join-Path $runnerRoot 'process.json'
$statePath = Join-Path $runnerRoot 'state.json'
$utf8 = New-Object Text.UTF8Encoding($false)

if (Test-Path -LiteralPath $pidPath) {
    $previous = [IO.File]::ReadAllText($pidPath, $utf8) | ConvertFrom-Json
    $process = Get-Process -Id $previous.pid -ErrorAction SilentlyContinue
    if ($process -and $process.Path -eq $previous.binary -and $process.StartTime.ToUniversalTime().Ticks -eq $previous.startedTicks) {
        if ($Stop) {
            Stop-Process -Id $process.Id
            Remove-Item -LiteralPath $pidPath
            Write-Host 'Windows Runner stopped.'
        } else {
            Write-Host "Windows Runner is already running (PID $($process.Id)). Stop it before changing its configuration."
        }
        return
    }
}
if ($Stop) { Write-Host 'Windows Runner is not running.'; return }

$config = if (Test-Path -LiteralPath $configPath) { [IO.File]::ReadAllText($configPath, $utf8) | ConvertFrom-Json } else { $null }
foreach ($key in @('ServerUrl', 'WorkspaceRoot', 'RunnerBinary', 'GateBinary', 'CliDirectory', 'PathEntries', 'Name', 'Concurrency')) {
    if (-not $PSBoundParameters.ContainsKey($key) -and $config -and $config.PSObject.Properties[$key]) {
        Set-Variable -Name $key -Value $config.$key
    }
}
if ($config -and $ServerUrl -and $config.ServerUrl.TrimEnd('/') -ne $ServerUrl.TrimEnd('/') -and (Test-Path -LiteralPath $statePath)) {
    throw 'This runner is already bound to another server. Use a separate checkout for a different runner identity.'
}
if (-not $ServerUrl) {
    $settingsFile = Join-Path $projectRoot '.local/windows/settings.json'
    if (Test-Path -LiteralPath $settingsFile) {
        $settings = [IO.File]::ReadAllText($settingsFile, $utf8) | ConvertFrom-Json
        $ServerUrl = 'http://127.0.0.1:' + $settings.httpPort
    } else { throw 'Pass -ServerUrl for the platform to connect to.' }
}
$uri = $null
if (-not [Uri]::TryCreate($ServerUrl, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -notin @('http', 'https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
    throw 'ServerUrl must be an HTTP(S) URL without embedded credentials, a query or a fragment.'
}
if (-not $RunnerBinary) { $RunnerBinary = Join-Path $projectRoot 'bin/agentflow-runner-windows-amd64.exe' }
if (-not $GateBinary) { $GateBinary = Join-Path $projectRoot 'bin/agentflow-gate-mcp-windows-amd64.exe' }
foreach ($key in @('RunnerBinary', 'GateBinary')) {
    $path = Get-Variable -Name $key -ValueOnly
    if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or [IO.Path]::GetExtension($path) -ne '.exe') { throw "Missing $key. Build the Windows binaries or pass -$key with an installed .exe path." }
    Set-Variable -Name $key -Value ([IO.Path]::GetFullPath($path))
}
foreach ($path in @($CliDirectory) + @($PathEntries)) {
    if ($path -and -not (Test-Path -LiteralPath $path -PathType Container)) { throw "Tool directory does not exist: $path" }
}
if (-not $WorkspaceRoot) { $WorkspaceRoot = Join-Path $runnerRoot 'workspaces' }
$WorkspaceRoot = [IO.Path]::GetFullPath($WorkspaceRoot)
if (-not $Name) { $Name = 'Windows-' + [Environment]::MachineName }
if ($Name -match '["\r\n]') { throw 'Runner Name cannot contain a quote or newline.' }
if (-not $BindCode -and $config -and $config.PSObject.Properties['bootstrapToken']) { $BindCode = $config.bootstrapToken }
if (-not $BindCode) { throw 'Generate a binding code in Settings > Execution machines, then pass -BindCode on the first start.' }
$null = New-Item -ItemType Directory -Path $runnerRoot -Force
$config = [ordered]@{
    ServerUrl = $ServerUrl.TrimEnd('/')
    WorkspaceRoot = $WorkspaceRoot
    RunnerBinary = $RunnerBinary
    GateBinary = $GateBinary
    CliDirectory = $CliDirectory
    PathEntries = @($PathEntries)
    Name = $Name
    Concurrency = $Concurrency
    bootstrapToken = $BindCode
}
[IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json), $utf8)
$keys = @('PATH', 'AGENTFLOW_SERVER_URL', 'AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN', 'AGENTFLOW_RUNNER_STATE_PATH', 'AGENTFLOW_RUNNER_WORKSPACE', 'AGENTFLOW_GATE_MCP_BINARY', 'AGENTFLOW_MANAGED_CLI_ROOT')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
try {
    $directories = @($CliDirectory) + @($PathEntries) | Where-Object { $_ } | ForEach-Object { [IO.Path]::GetFullPath($_) }
    $env:PATH = (@($directories) + @($env:PATH)) -join [IO.Path]::PathSeparator
    $env:AGENTFLOW_SERVER_URL = $config.ServerUrl
    $env:AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN = $BindCode
    $env:AGENTFLOW_RUNNER_STATE_PATH = $statePath
    $env:AGENTFLOW_RUNNER_WORKSPACE = $WorkspaceRoot
    $env:AGENTFLOW_GATE_MCP_BINARY = $GateBinary
    $env:AGENTFLOW_MANAGED_CLI_ROOT = ''
    $process = Start-Process -FilePath $RunnerBinary -ArgumentList @('-auto-update=false', '-name', ('"' + $Name + '"'), '-concurrency', [string]$Concurrency) -WorkingDirectory $runnerRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runnerRoot 'stdout.log') -RedirectStandardError (Join-Path $runnerRoot 'stderr.log')
    [IO.File]::WriteAllText($pidPath, (@{ pid = $process.Id; startedTicks = $process.StartTime.ToUniversalTime().Ticks; binary = $RunnerBinary } | ConvertTo-Json), $utf8)
    Start-Sleep -Seconds 2
    $process.Refresh()
    if ($process.HasExited) { throw 'Runner exited during startup. Check .local/windows/runner/stderr.log and renew an expired binding code.' }
    Write-Host "Windows Runner started (PID $($process.Id)) using the current Windows user environment."
    Write-Host 'Select this execution machine in the platform and choose local authentication for the installed CLI.'
} finally {
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
}
