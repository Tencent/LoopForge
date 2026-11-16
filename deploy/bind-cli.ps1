<#
.SYNOPSIS
Connects, updates, stops or uninstalls a Windows execution machine.
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -BindCode '<binding-code>'
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -Stop
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -Update
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -Rebind -BindCode '<fresh-binding-code>'
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -Uninstall
#>
[CmdletBinding()]
param(
    [string]$ServerUrl,
    [string]$BindCode,
    [string]$InstallDirectory,
    [string]$WorkspaceRoot,
    [string]$CliDirectory,
    [string[]]$PathEntries,
    [string]$Name,
    [ValidateRange(1, 32)][int]$Concurrency = 1,
    [switch]$NoStart,
    [switch]$Stop,
    [switch]$Update,
    [switch]$Rebind,
    [switch]$Uninstall,
    [switch]$LocalOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$utf8 = New-Object Text.UTF8Encoding($false)
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Use install.sh on macOS or Linux.' }
if ($LocalOnly -and -not $Uninstall) { throw '-LocalOnly requires -Uninstall.' }
if (@($Stop, $Update, $Rebind, $Uninstall | Where-Object { $_ }).Count -gt 1) { throw 'Choose only one of -Stop, -Update, -Rebind or -Uninstall.' }
if ($Rebind -and -not $BindCode) { throw '-Rebind requires a fresh -BindCode from the same platform account.' }
if ($Stop -or $Update -or $Rebind -or $Uninstall) {
    $restricted = @('WorkspaceRoot', 'CliDirectory', 'PathEntries', 'Name', 'Concurrency')
    if (-not $Rebind) { $restricted += 'BindCode' }
    foreach ($key in $restricted) {
        if ($PSBoundParameters.ContainsKey($key)) { throw "-$key cannot be combined with a lifecycle operation. Use the saved installation configuration." }
    }
    if ($NoStart -and -not $Update) { throw '-NoStart can only be used with installation or -Update.' }
}

# 校验下载地址，禁止携带凭据或在安装时切换平台。
function Get-PlatformUrl {
    param([string]$Value, [switch]$OfflineCleanup)
    $uri = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -notin @('http', 'https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
        throw 'ServerUrl must be an HTTP(S) URL without embedded credentials, a query or a fragment.'
    }
    if ($uri.Scheme -eq 'http' -and -not $OfflineCleanup) {
        $address = $null
        $loopback = $uri.DnsSafeHost -ieq 'localhost' -or ([Net.IPAddress]::TryParse($uri.DnsSafeHost, [ref]$address) -and [Net.IPAddress]::IsLoopback($address))
        if (-not $loopback) { throw 'ServerUrl requires HTTPS unless its host is a loopback address (localhost, 127.0.0.0/8 or ::1).' }
    }
    return $uri.AbsoluteUri.TrimEnd('/')
}

# 使用系统原生架构，兼容 32 位 PowerShell 运行于 64 位 Windows。
function Get-WindowsArchitecture {
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
    switch ($architecture.ToLowerInvariant()) {
        'amd64' { return 'amd64' }
        'arm64' { return 'arm64' }
        default { throw 'Only Windows amd64 and arm64 are supported.' }
    }
}

# 校验摘要和 PE 架构，防止错误页或不匹配的可执行文件被启动。
function Assert-WindowsArtifact {
    param([string]$Path, [string]$Sha256, [string]$Architecture)
    $stream = [IO.File]::OpenRead($Path)
    $reader = New-Object IO.BinaryReader($stream)
    $hash = [Security.Cryptography.SHA256]::Create()
    try {
        $actualHash = ([BitConverter]::ToString($hash.ComputeHash($stream))).Replace('-', '')
        if ($actualHash -ine $Sha256) { throw 'Downloaded binary checksum mismatch.' }
        $stream.Position = 0
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) { throw 'Downloaded file is not a Windows executable.' }
        $stream.Position = 60
        $offset = $reader.ReadUInt32()
        if ($offset -gt $stream.Length - 6) { throw 'Downloaded Windows executable has an invalid PE header.' }
        $stream.Position = $offset
        if ($reader.ReadUInt32() -ne 0x00004550) { throw 'Downloaded Windows executable has an invalid PE signature.' }
        $expectedMachine = if ($Architecture -eq 'arm64') { 0xAA64 } else { 0x8664 }
        if ($reader.ReadUInt16() -ne $expectedMachine) { throw 'Downloaded Windows executable architecture does not match this machine.' }
    } finally { $reader.Dispose(); $hash.Dispose() }
}

# Docker 沙箱使用同架构 Linux helper，必须验证 ELF，不能误发宿主 PE。
function Assert-LinuxArtifact {
    param([string]$Path, [string]$Sha256, [string]$Architecture)
    $stream = [IO.File]::OpenRead($Path)
    $reader = New-Object IO.BinaryReader($stream)
    $hash = [Security.Cryptography.SHA256]::Create()
    try {
        $actualHash = ([BitConverter]::ToString($hash.ComputeHash($stream))).Replace('-', '')
        if ($actualHash -ine $Sha256) { throw 'Downloaded Linux helper checksum mismatch.' }
        $stream.Position = 0
        if ($stream.Length -lt 64 -or $reader.ReadUInt32() -ne 0x464C457F) { throw 'Downloaded Linux helper is not an ELF executable.' }
        if ($reader.ReadByte() -ne 2 -or $reader.ReadByte() -ne 1 -or $reader.ReadByte() -ne 1) { throw 'Downloaded Linux helper must be a 64-bit little-endian ELF executable.' }
        $stream.Position = 18
        $expectedMachine = if ($Architecture -eq 'arm64') { 183 } else { 62 }
        if ($reader.ReadUInt16() -ne $expectedMachine) { throw 'Downloaded Linux helper architecture does not match this machine.' }
    } finally { $reader.Dispose(); $hash.Dispose() }
}

# 所有写入与删除都限制在安装目录；拒绝联接或符号链接，避免清理越界。
function Assert-InstallPath {
    param([string]$Path)
    $absolute = [IO.Path]::GetFullPath($Path)
    $prefix = $InstallDirectory.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if ($absolute -ine $InstallDirectory -and -not $absolute.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Managed path escapes the installation directory.' }
    $current = $absolute
    while ($current -and ($current -ieq $InstallDirectory -or $current.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase))) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Managed installation paths cannot be junctions or symbolic links.' }
        }
        $current = [IO.Path]::GetDirectoryName($current)
    }
    return $absolute
}

function Assert-ManagedBinary {
    param([string]$Path, [string]$Name)
    $absolute = Assert-InstallPath -Path $Path
    $relative = $absolute.Substring(($InstallDirectory.TrimEnd('\', '/') + '\').Length)
    if ($relative -notmatch ('^bin[\\/][a-f0-9]{16}-[a-f0-9]{16}(?:-[a-f0-9]{16})?[\\/]' + [Regex]::Escape($Name) + '\.exe$')) { throw 'Saved binary does not belong to this managed installation.' }
    return $absolute
}

function Write-AtomicJSON {
    param([string]$Path, $Value)
    Write-AtomicBytes -Path $Path -Value $utf8.GetBytes(($Value | ConvertTo-Json))
}

function Write-AtomicBytes {
    param([string]$Path, [byte[]]$Value)
    $null = Assert-InstallPath -Path $Path
    $temporary = Join-Path $InstallDirectory ('.write-' + [Guid]::NewGuid().ToString('N'))
    try {
        [IO.File]::WriteAllBytes($temporary, $Value)
        if (Test-Path -LiteralPath $Path) { [IO.File]::Replace($temporary, $Path, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $Path) }
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary } }
}

function Read-SavedJSON {
    param([string]$Path)
    try { return [IO.File]::ReadAllText($Path, $utf8) | ConvertFrom-Json }
    catch { throw ('Cannot read saved ' + [IO.Path]::GetFileName($Path) + '. Restore valid JSON before continuing.') }
}

# PID 会复用，必须同时核对安装配置、可执行路径及进程启动时间。
function Get-OwnedRunner {
    param($SavedConfig)
    if (-not (Test-Path -LiteralPath $pidPath)) { return $null }
    $previous = Read-SavedJSON -Path $pidPath
    $binary = Assert-ManagedBinary -Path $SavedConfig.RunnerBinary -Name 'agentflow-runner'
    if ([IO.Path]::GetFullPath($previous.binary) -ine $binary) { throw 'Process record does not match the saved managed binary.' }
    $process = Get-Process -Id $previous.pid -ErrorAction SilentlyContinue
    if ($process -and $process.Path -ieq $binary -and $process.StartTime.ToUniversalTime().Ticks -eq $previous.startedTicks) { return $process }
    return $null
}

function Stop-OwnedRunner {
    param($Process)
    if ($Process) {
        Stop-Process -InputObject $Process -ErrorAction Stop
        if (-not $Process.WaitForExit(10000)) { throw 'Runner did not stop. Configuration and files retained.' }
    }
    if (Test-Path -LiteralPath $pidPath) { Remove-Item -LiteralPath $pidPath }
}

function Read-RunnerSession {
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { return $null }
    try {
        $session = Read-SavedJSON -Path $statePath
        if ($session.PSObject.Properties['runnerId'] -and $session.runnerId -and $session.PSObject.Properties['sessionToken'] -and $session.sessionToken) { return $session }
    } catch { }
    return $null
}

# 只读预检验证会话，不领取任务、不更新 CLI 清单，也不消费正在排队的工作。
function Test-RunnerSession {
    param($Settings, $Session)
    $body = '{"availableRuntimes":["mock"],"capacity":1,"preflight":true,"noWait":true}'
    $null = Invoke-RestMethod -Uri ($Settings.ServerUrl + '/runners/tasks/claim') -Method Post -Headers @{ Authorization = 'Bearer ' + $Session.sessionToken } -ContentType 'application/json' -Body $body -MaximumRedirection 0 -TimeoutSec 3
}

function Wait-RunnerReady {
    param($Process, $Settings, [string]$ExpectedRunnerID)
    # CLI discovery and registration can each wait on slow local tools or HTTP.
    $deadline = (Get-Date).ToUniversalTime().AddSeconds(120)
    do {
        $Process.Refresh()
        if ($Process.HasExited) { throw 'Runner exited during startup. Check stderr.log; use -Rebind -BindCode with a fresh code if its saved session has expired or been revoked.' }
        $session = Read-RunnerSession
        if ($session) {
            if ($ExpectedRunnerID -and $session.runnerId -cne $ExpectedRunnerID) { throw 'The platform returned a different Runner identity. The previous configuration and workspace will be retained.' }
            try {
                Test-RunnerSession -Settings $Settings -Session $session
                $Process.Refresh()
                if (-not $Process.HasExited) { return }
            } catch {
                $status = 0
                if ($_.Exception.PSObject.Properties['Response'] -and $_.Exception.Response) {
                    try { $status = [int]$_.Exception.Response.StatusCode } catch { }
                }
                if ($status -eq 401 -or $status -eq 403) { throw 'Runner session was rejected. Run -Rebind -BindCode with a fresh code from the same platform account.' }
            }
        }
        Start-Sleep -Milliseconds 200
    } while ((Get-Date).ToUniversalTime() -lt $deadline)
    throw 'Runner did not confirm its platform session within 120 seconds. Check stderr.log and the platform connection, then retry.'
}

function Start-ConfiguredRunner {
    param($Settings, [ref]$StartedProcess, [string]$ExpectedRunnerID)
    $keys = @('PATH', 'AGENTFLOW_SERVER_URL', 'AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN', 'AGENTFLOW_RUNNER_STATE_PATH', 'AGENTFLOW_RUNNER_WORKSPACE', 'AGENTFLOW_RUNNER_CONCURRENCY', 'AGENTFLOW_GATE_MCP_BINARY', 'AGENTFLOW_MANAGED_CLI_ROOT')
    $saved = @{}
    foreach ($key in $keys) { $saved[$key] = @{ Exists = Test-Path -LiteralPath "Env:$key"; Value = [Environment]::GetEnvironmentVariable($key, 'Process') } }
    $process = $null
    try {
        $directories = @($Settings.CliDirectory) + @($Settings.PathEntries) | Where-Object { $_ } | ForEach-Object { [IO.Path]::GetFullPath($_) }
        $env:PATH = (@($directories) + @($env:PATH)) -join [IO.Path]::PathSeparator
        $env:AGENTFLOW_SERVER_URL = $Settings.ServerUrl
        $env:AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN = $Settings.bootstrapToken
        $env:AGENTFLOW_RUNNER_STATE_PATH = $statePath
        $env:AGENTFLOW_RUNNER_WORKSPACE = $Settings.WorkspaceRoot
        $env:AGENTFLOW_RUNNER_CONCURRENCY = [string]$Settings.Concurrency
        $env:AGENTFLOW_GATE_MCP_BINARY = $Settings.GateBinary
        Remove-Item -LiteralPath 'Env:AGENTFLOW_MANAGED_CLI_ROOT' -ErrorAction SilentlyContinue
        # 原生命令行的闭引号前需将尾部反斜杠加倍，防止名称吞掉后续参数。
        $quotedName = '"' + ($Settings.Name -replace '(\\+)$', '${1}${1}') + '"'
        $stdout = Assert-InstallPath -Path (Join-Path $InstallDirectory 'stdout.log')
        $stderr = Assert-InstallPath -Path (Join-Path $InstallDirectory 'stderr.log')
        $process = Start-Process -FilePath $Settings.RunnerBinary -ArgumentList @('-auto-update=false', '-name', $quotedName, '-concurrency', [string]$Settings.Concurrency) -WorkingDirectory $InstallDirectory -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        if ($StartedProcess) { $StartedProcess.Value = $process }
        Write-AtomicJSON -Path $pidPath -Value @{ pid = $process.Id; startedTicks = $process.StartTime.ToUniversalTime().Ticks; binary = $Settings.RunnerBinary }
        Start-Sleep -Seconds 2
        Wait-RunnerReady -Process $process -Settings $Settings -ExpectedRunnerID $ExpectedRunnerID
        return $process
    } catch {
        if ($process) {
            $process.Refresh()
            if (-not $process.HasExited) { Stop-OwnedRunner -Process $process }
            elseif (Test-Path -LiteralPath $pidPath) { Remove-Item -LiteralPath $pidPath }
        }
        throw
    } finally {
        foreach ($key in $keys) {
            if ($saved[$key].Exists) { Set-Item -LiteralPath "Env:$key" -Value $saved[$key].Value }
            else { Remove-Item -LiteralPath "Env:$key" -ErrorAction SilentlyContinue }
        }
    }
}

if ($ServerUrl) { $ServerUrl = Get-PlatformUrl -Value $ServerUrl -OfflineCleanup:($Stop -or ($Uninstall -and $LocalOnly)) }
if (-not $InstallDirectory) {
    if (-not $ServerUrl) { throw 'Pass -ServerUrl, or -InstallDirectory for an existing installation.' }
    $hash = [Security.Cryptography.SHA256]::Create()
    try { $instance = ([BitConverter]::ToString($hash.ComputeHash($utf8.GetBytes($ServerUrl)))).Replace('-', '').Substring(0, 16).ToLowerInvariant() }
    finally { $hash.Dispose() }
    $InstallDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) ('LoopForge/runners/' + $instance)
}
$InstallDirectory = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\', '/')
if (-not [IO.Path]::GetFileName($InstallDirectory) -or $InstallDirectory -eq [IO.Path]::GetPathRoot($InstallDirectory).TrimEnd('\', '/')) { throw 'InstallDirectory must be a dedicated directory, not a drive root.' }
$null = Assert-InstallPath -Path $InstallDirectory
$configPath = Join-Path $InstallDirectory 'config.json'
$statePath = Join-Path $InstallDirectory 'state.json'
$pidPath = Join-Path $InstallDirectory 'process.json'
if (($Stop -or $Uninstall) -and -not (Test-Path -LiteralPath $InstallDirectory)) { Write-Host 'Windows Runner is not installed or running.'; return }
if ($Update -and -not (Test-Path -LiteralPath $configPath)) { throw 'No saved Windows Runner installation was found to update.' }
if ($Rebind -and -not (Test-Path -LiteralPath $configPath)) { throw 'No saved Windows Runner installation was found to rebind.' }
$null = New-Item -ItemType Directory -Force -Path $InstallDirectory
# Windows PowerShell 在目录创建后才可能展开其 8.3 别名，统一后续路径。
$InstallDirectory = [IO.Path]::GetFullPath((Get-Item -LiteralPath $InstallDirectory).FullName)
$configPath = Join-Path $InstallDirectory 'config.json'
$statePath = Join-Path $InstallDirectory 'state.json'
$pidPath = Join-Path $InstallDirectory 'process.json'
# 同一安装目录仅允许一个安装或停止操作，避免下载和进程记录互相覆盖。
$null = Assert-InstallPath -Path (Join-Path $InstallDirectory 'operation.lock')
$operationLock = [IO.File]::Open((Join-Path $InstallDirectory 'operation.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try {
    foreach ($path in @($configPath, $statePath, $pidPath)) { $null = Assert-InstallPath -Path $path }
    $config = if (Test-Path -LiteralPath $configPath) { Read-SavedJSON -Path $configPath } else { $null }
    if ($config) {
        # 旧版本曾允许远端 HTTP；离线停止/本地卸载仍应可用，但不得再向它发送请求。
        $savedServer = Get-PlatformUrl -Value $config.ServerUrl -OfflineCleanup:($Stop -or ($Uninstall -and $LocalOnly))
        if ($ServerUrl -and $ServerUrl -cne $savedServer) { throw 'This installation belongs to another server. Use a separate InstallDirectory.' }
        $ServerUrl = $savedServer
        foreach ($key in @('WorkspaceRoot', 'CliDirectory', 'PathEntries', 'Name', 'Concurrency')) {
            if (-not $PSBoundParameters.ContainsKey($key) -and $config.PSObject.Properties[$key]) { Set-Variable -Name $key -Value $config.$key }
        }
        if ($config.PSObject.Properties['UninstallPending'] -and $config.UninstallPending -and -not $Uninstall) { throw 'This Runner is being uninstalled. Retry -Uninstall to finish local cleanup.' }
    }
    if (-not $config -and (Test-Path -LiteralPath $statePath)) { throw 'Existing runner state has no platform configuration. Restore config.json before continuing.' }
    if (-not $config -and (Test-Path -LiteralPath $pidPath)) { throw 'Existing process record has no platform configuration. Restore config.json before continuing.' }
    if (-not $config -and ($Stop -or $Uninstall)) { Write-Host 'Windows Runner is not installed or running.'; return }
    if (-not $ServerUrl) { throw 'Pass -ServerUrl on the first installation.' }
    if ($config) {
        $savedRunner = Assert-ManagedBinary -Path $config.RunnerBinary -Name 'agentflow-runner'
        $savedGate = Assert-ManagedBinary -Path $config.GateBinary -Name 'agentflow-gate-mcp'
        if ([IO.Path]::GetDirectoryName($savedRunner) -ine [IO.Path]::GetDirectoryName($savedGate)) { throw 'Saved Runner and Gate binaries must belong to the same managed version.' }
    }
    $previousProcess = if ($config) { Get-OwnedRunner -SavedConfig $config } else { $null }
    if ($Rebind -and $previousProcess) { throw 'Stop the installed Runner with -Stop before using -Rebind.' }
    $rebindState = $null
    $rebindStateBytes = $null
    $rebindConfigBytes = $null
    if ($Rebind) {
        if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { throw '-Rebind requires saved state containing the existing Runner identity.' }
        $rebindState = Read-SavedJSON -Path $statePath
        if (-not $rebindState.PSObject.Properties['runnerId'] -or -not $rebindState.runnerId) { throw '-Rebind requires saved state containing the existing Runner identity.' }
        $rebindStateBytes = [IO.File]::ReadAllBytes($statePath)
        $rebindConfigBytes = [IO.File]::ReadAllBytes($configPath)
    }
    if ($Stop) {
        # 身份不匹配的记录不用于终止进程；保留记录供用户核对。
        if ($previousProcess) { Stop-OwnedRunner -Process $previousProcess; Write-Host 'Windows Runner stopped. Configuration and workspaces retained.' }
        else { Write-Host 'Windows Runner is not running. Configuration and workspaces retained.' }
        return
    }
    if ($Uninstall) {
        # 只清理可识别的托管二进制。工作区、额外文件以及外部目录恒保留。
        $retainedWorkspace = [IO.Path]::GetFullPath($config.WorkspaceRoot).TrimEnd('\', '/')
        # 最后删除配置，若清理被文件占用打断，重试时仍能识别已注销状态。
        $managedFiles = @('state.json', 'process.json', 'install.ps1', 'stdout.log', 'stderr.log') | ForEach-Object { Join-Path $InstallDirectory $_ }
        $binRoot = Assert-InstallPath -Path (Join-Path $InstallDirectory 'bin')
        if (Test-Path -LiteralPath $binRoot) {
            foreach ($directory in Get-ChildItem -LiteralPath $binRoot -Directory -Force) {
                if ($directory.Name -notmatch '^[a-f0-9]{16}-[a-f0-9]{16}(?:-[a-f0-9]{16})?$') { continue }
                foreach ($name in @('agentflow-runner.exe', 'agentflow-gate-mcp.exe', 'agentflow-gate-mcp-linux-amd64', 'agentflow-gate-mcp-linux-arm64')) { $managedFiles += Join-Path $directory.FullName $name }
            }
        }
        $managedFiles += $configPath
        # 先完整校验清理清单，再删除，防止中途遇到链接时只删了一半。
        foreach ($path in $managedFiles) { $null = Assert-InstallPath -Path $path }
        # 先确认配置可原子替换，避免文件占用导致远端已注销却无法记录清理状态。
        # 此时尚未停止进程或撤销凭据，失败后原安装仍可继续使用。
        Write-AtomicJSON -Path $configPath -Value $config
        Stop-OwnedRunner -Process $previousProcess
        if (-not ($config.PSObject.Properties['UninstallPending'] -and $config.UninstallPending)) {
            if (-not $LocalOnly -and (Test-Path -LiteralPath $statePath)) {
                $state = Read-SavedJSON -Path $statePath
                if (-not $state.PSObject.Properties['sessionToken'] -or -not $state.sessionToken) { throw 'Runner session is missing. Configuration and state retained. Use -Uninstall -LocalOnly for local cleanup and remove the offline machine in the platform.' }
                try {
                    $null = Invoke-RestMethod -Uri ($ServerUrl + '/runners/deregister') -Method Post -Headers @{ Authorization = 'Bearer ' + $state.sessionToken } -ContentType 'application/json' -Body '{}' -MaximumRedirection 0 -TimeoutSec 20
                } catch { throw 'Platform deregistration failed. Runner is stopped; configuration, state and files retained. Retry -Uninstall, or use -Uninstall -LocalOnly for local cleanup and remove the offline machine in the platform.' }
            }
            $pending = if ($LocalOnly) { 'local-only' } else { 'deregistered' }
            $config | Add-Member -NotePropertyName UninstallPending -NotePropertyValue $pending -Force
            Write-AtomicJSON -Path $configPath -Value $config
        }
        foreach ($path in $managedFiles) {
            $absolute = [IO.Path]::GetFullPath($path)
            if (Test-Path -LiteralPath $absolute -PathType Leaf) { Remove-Item -LiteralPath $absolute }
        }
        Write-Host "Windows Runner uninstalled. Workspaces and other user files retained in $retainedWorkspace."
        if ($LocalOnly -or $config.UninstallPending -eq 'local-only') { Write-Host 'Local cleanup only: remove the offline execution machine in the platform to finish unbinding.' }
        return
    }
    if ($previousProcess -and -not $Update) { Write-Host 'Windows Runner is already running. Use -Update to update it, or -Stop before changing options.'; return }
    if ($Update -and (-not (Test-Path -LiteralPath $config.RunnerBinary -PathType Leaf) -or -not (Test-Path -LiteralPath $config.GateBinary -PathType Leaf))) { throw 'The saved installation binaries are missing; restore them before updating.' }
    if (-not $Rebind -and $BindCode -and $config -and (Test-Path -LiteralPath $statePath) -and $BindCode -cne $config.bootstrapToken) {
        throw 'This installation is already bound. Restart without -BindCode, or use a separate InstallDirectory for a new binding.'
    }
    if (-not $BindCode -and $config) { $BindCode = $config.bootstrapToken }
    if (-not $BindCode) { throw 'Generate a binding code in Settings > Execution machines and pass -BindCode.' }
    foreach ($path in @($CliDirectory) + @($PathEntries)) {
        if ($path -and -not (Test-Path -LiteralPath $path -PathType Container)) { throw 'A CLI or tool directory does not exist.' }
    }
    if ($CliDirectory) { $CliDirectory = [IO.Path]::GetFullPath($CliDirectory) }
    $PathEntries = @($PathEntries | Where-Object { $_ } | ForEach-Object { [IO.Path]::GetFullPath($_) })
    if (-not $WorkspaceRoot) { $WorkspaceRoot = Join-Path $InstallDirectory 'workspaces' }
    $WorkspaceRoot = [IO.Path]::GetFullPath($WorkspaceRoot)
    if (-not $Name) { $Name = 'Windows-' + [Environment]::MachineName }
    if ($Name -match '["\r\n]') { throw 'Runner Name cannot contain a quote or newline.' }
    $architecture = Get-WindowsArchitecture
    if ($Update -and ([IO.Path]::GetFileName([IO.Path]::GetDirectoryName($config.GateBinary)) -match '^[a-f0-9]{16}-[a-f0-9]{16}-[a-f0-9]{16}$')) {
        $savedLinuxGate = Assert-InstallPath -Path (Join-Path ([IO.Path]::GetDirectoryName($config.GateBinary)) ('agentflow-gate-mcp-linux-' + $architecture))
        if (-not (Test-Path -LiteralPath $savedLinuxGate -PathType Leaf)) { throw 'The saved Linux Gate helper is missing; restore it before updating.' }
    }
    $downloads = @()
    $previousTls = [Net.ServicePointManager]::SecurityProtocol
    try {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls -bor [Net.SecurityProtocolType]::Tls12
        $artifacts = @(
            @{ name = 'agentflow-runner'; os = 'windows'; fileName = 'agentflow-runner.exe' },
            @{ name = 'agentflow-gate-mcp'; os = 'windows'; fileName = 'agentflow-gate-mcp.exe' },
            @{ name = 'agentflow-gate-mcp'; os = 'linux'; fileName = 'agentflow-gate-mcp-linux-' + $architecture }
        )
        foreach ($artifact in $artifacts) {
            $endpoint = $ServerUrl + '/runners/' + $artifact.name + '?os=' + $artifact.os + '&arch=' + $architecture
            $metadata = Invoke-RestMethod -Uri ($endpoint + '&metadata=1') -Method Get -MaximumRedirection 0 -TimeoutSec 120
            if (-not $metadata.sha256 -or $metadata.sha256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Platform returned invalid binary metadata.' }
            $temporary = Join-Path $InstallDirectory ('.download-' + [Guid]::NewGuid().ToString('N'))
            $downloads += @{ fileName = $artifact.fileName; os = $artifact.os; path = $temporary; sha256 = $metadata.sha256.ToLowerInvariant() }
            Invoke-WebRequest -Uri $endpoint -OutFile $temporary -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 120
            if ($artifact.os -eq 'linux') { Assert-LinuxArtifact -Path $temporary -Sha256 $metadata.sha256 -Architecture $architecture }
            else { Assert-WindowsArtifact -Path $temporary -Sha256 $metadata.sha256 -Architecture $architecture }
        }
        # 每个版本使用独立目录，不替换仍可能被旧任务占用的 exe。
        $version = ($downloads | ForEach-Object { $_.sha256.Substring(0, 16) }) -join '-'
        $binaryDirectory = Assert-InstallPath -Path (Join-Path $InstallDirectory ('bin/' + $version))
        $null = New-Item -ItemType Directory -Force -Path $binaryDirectory
        foreach ($download in $downloads) {
            $destination = Assert-InstallPath -Path (Join-Path $binaryDirectory $download.fileName)
            if (Test-Path -LiteralPath $destination) {
                if ($download.os -eq 'linux') { Assert-LinuxArtifact -Path $destination -Sha256 $download.sha256 -Architecture $architecture }
                else { Assert-WindowsArtifact -Path $destination -Sha256 $download.sha256 -Architecture $architecture }
            }
            else { Move-Item -LiteralPath $download.path -Destination $destination }
        }
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls
        foreach ($download in $downloads) { if (Test-Path -LiteralPath $download.path) { Remove-Item -LiteralPath $download.path } }
    }
    $runnerBinary = Join-Path $binaryDirectory 'agentflow-runner.exe'
    $gateBinary = Join-Path $binaryDirectory 'agentflow-gate-mcp.exe'
    $candidateConfig = [ordered]@{ ServerUrl = $ServerUrl; WorkspaceRoot = $WorkspaceRoot; CliDirectory = $CliDirectory; PathEntries = @($PathEntries); Name = $Name; Concurrency = $Concurrency; bootstrapToken = $BindCode; RunnerBinary = $runnerBinary; GateBinary = $gateBinary }
    # 保存通用脚本以便离线查看和以后停止，脚本本身不包含绑定凭据。
    if ($PSCommandPath) {
        $savedScript = Assert-InstallPath -Path (Join-Path $InstallDirectory 'install.ps1')
        if ([IO.Path]::GetFullPath($PSCommandPath) -ine $savedScript) { [IO.File]::WriteAllBytes($savedScript, [IO.File]::ReadAllBytes($PSCommandPath)) }
    }
    $shouldStart = -not $NoStart -and (-not $Update -or $previousProcess)
    $candidateProcess = $null
    $stateWasStaged = $false
    # 首次接入保留配置供失败后重试；已有安装仅在新版启动成功后提交。
    if (-not $config) { Write-AtomicJSON -Path $configPath -Value $candidateConfig }
    if ($previousProcess -or $Update) { Stop-OwnedRunner -Process $previousProcess }
    try {
        if ($Rebind) {
            # 在注销旧凭据前确认本地配置可以提交；服务端会立即轮换会话，无法撤销。
            Write-AtomicBytes -Path $configPath -Value $rebindConfigBytes
            Write-AtomicJSON -Path $statePath -Value @{ runnerId = $rebindState.runnerId; sessionToken = '' }
            $stateWasStaged = $true
        }
        if ($shouldStart) { $null = Start-ConfiguredRunner -Settings $candidateConfig -StartedProcess ([ref]$candidateProcess) -ExpectedRunnerID $(if ($Rebind) { $rebindState.runnerId } else { '' }) }
        Write-AtomicJSON -Path $configPath -Value $candidateConfig
    } catch {
        $updateFailure = $_
        if ($candidateProcess) {
            $candidateProcess.Refresh()
            if (-not $candidateProcess.HasExited) { Stop-OwnedRunner -Process $candidateProcess }
        }
        if ($Rebind -and $stateWasStaged) {
            $issuedSession = Read-RunnerSession
            if ($issuedSession -and $issuedSession.runnerId -ceq $rebindState.runnerId -and (-not $rebindState.PSObject.Properties['sessionToken'] -or $issuedSession.sessionToken -cne $rebindState.sessionToken)) {
                throw 'Rebind failed after the platform issued a replacement session. The new session, previous configuration and workspace are retained. Restart the saved installation without -Rebind or -BindCode.'
            }
            Write-AtomicBytes -Path $statePath -Value $rebindStateBytes
        }
        if ($previousProcess) {
            try { $null = Start-ConfiguredRunner -Settings $config }
            catch { throw 'Update failed and the previous Runner could not restart. Previous configuration, binary and workspaces retained; restart the saved installation manually.' }
            throw 'Update failed. Previous Runner restarted; configuration, identity and workspaces retained.'
        }
        throw $updateFailure
    }
    if (-not $shouldStart) { Write-Host "Windows Runner installed in $InstallDirectory. It remains stopped; configuration, identity and workspaces retained."; return }
    Write-Host "Windows Runner started using the current Windows user environment. Logs: $InstallDirectory"
    Write-Host 'Select this execution machine in the platform and choose local authentication for Codex or Claude Code.'
} finally { $operationLock.Dispose() }
