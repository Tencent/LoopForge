<#
.SYNOPSIS
Installs or stops a Windows execution machine without a source checkout.
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -BindCode '<binding-code>'
.EXAMPLE
.\install.ps1 -ServerUrl https://loopforge.example.com -Stop
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
    [switch]$Stop
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$utf8 = New-Object Text.UTF8Encoding($false)
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Use install.sh on macOS or Linux.' }

# 校验下载地址，禁止携带凭据或在安装时切换平台。
function Get-PlatformUrl {
    param([string]$Value)
    $uri = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -notin @('http', 'https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
        throw 'ServerUrl must be an HTTP(S) URL without embedded credentials, a query or a fragment.'
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

if ($ServerUrl) { $ServerUrl = Get-PlatformUrl -Value $ServerUrl }
if (-not $InstallDirectory) {
    if (-not $ServerUrl) { throw 'Pass -ServerUrl, or -InstallDirectory for an existing installation.' }
    $hash = [Security.Cryptography.SHA256]::Create()
    try { $instance = ([BitConverter]::ToString($hash.ComputeHash($utf8.GetBytes($ServerUrl)))).Replace('-', '').Substring(0, 16).ToLowerInvariant() }
    finally { $hash.Dispose() }
    $InstallDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) ('LoopForge/runners/' + $instance)
}
$InstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
$configPath = Join-Path $InstallDirectory 'config.json'
$statePath = Join-Path $InstallDirectory 'state.json'
$pidPath = Join-Path $InstallDirectory 'process.json'
if ($Stop -and -not (Test-Path -LiteralPath $InstallDirectory)) { Write-Host 'Windows Runner is not running.'; return }
$null = New-Item -ItemType Directory -Force -Path $InstallDirectory
# 同一安装目录仅允许一个安装或停止操作，避免下载和进程记录互相覆盖。
$operationLock = [IO.File]::Open((Join-Path $InstallDirectory 'operation.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try {
    $config = if (Test-Path -LiteralPath $configPath) { [IO.File]::ReadAllText($configPath, $utf8) | ConvertFrom-Json } else { $null }
    if ($config) {
        $savedServer = Get-PlatformUrl -Value $config.ServerUrl
        if ($ServerUrl -and $ServerUrl -cne $savedServer) { throw 'This installation belongs to another server. Use a separate InstallDirectory.' }
        $ServerUrl = $savedServer
        foreach ($key in @('WorkspaceRoot', 'CliDirectory', 'PathEntries', 'Name', 'Concurrency')) {
            if (-not $PSBoundParameters.ContainsKey($key) -and $config.PSObject.Properties[$key]) { Set-Variable -Name $key -Value $config.$key }
        }
    }
    if (-not $ServerUrl) { throw 'Pass -ServerUrl on the first installation.' }
    if (-not $config -and (Test-Path -LiteralPath $statePath)) { throw 'Existing runner state has no platform configuration. Restore config.json before continuing.' }
    if (Test-Path -LiteralPath $pidPath) {
        $previous = [IO.File]::ReadAllText($pidPath, $utf8) | ConvertFrom-Json
        $process = Get-Process -Id $previous.pid -ErrorAction SilentlyContinue
        if ($process -and $process.Path -eq $previous.binary -and $process.StartTime.ToUniversalTime().Ticks -eq $previous.startedTicks) {
            if ($Stop) {
                Stop-Process -Id $process.Id
                Remove-Item -LiteralPath $pidPath
                Write-Host 'Windows Runner stopped. Configuration and workspaces retained.'
            } else { Write-Host 'Windows Runner is already running. Stop it before updating or changing options.' }
            return
        }
    }
    if ($Stop) { Write-Host 'Windows Runner is not running. Configuration and workspaces retained.'; return }
    if ($BindCode -and $config -and (Test-Path -LiteralPath $statePath) -and $BindCode -cne $config.bootstrapToken) {
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
    $downloads = @()
    $previousTls = [Net.ServicePointManager]::SecurityProtocol
    try {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls -bor [Net.SecurityProtocolType]::Tls12
        foreach ($artifact in @('agentflow-runner', 'agentflow-gate-mcp')) {
            $endpoint = $ServerUrl + '/runners/' + $artifact + '?os=windows&arch=' + $architecture
            $metadata = Invoke-RestMethod -Uri ($endpoint + '&metadata=1') -Method Get -MaximumRedirection 0 -TimeoutSec 120
            if (-not $metadata.sha256 -or $metadata.sha256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Platform returned invalid binary metadata.' }
            $temporary = Join-Path $InstallDirectory ('.download-' + [Guid]::NewGuid().ToString('N'))
            $downloads += @{ name = $artifact; path = $temporary; sha256 = $metadata.sha256.ToLowerInvariant() }
            Invoke-WebRequest -Uri $endpoint -OutFile $temporary -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 120
            Assert-WindowsArtifact -Path $temporary -Sha256 $metadata.sha256 -Architecture $architecture
        }
        # 每组二进制使用独立目录，不替换仍可能被旧任务占用的 exe。
        $version = $downloads[0].sha256.Substring(0, 16) + '-' + $downloads[1].sha256.Substring(0, 16)
        $binaryDirectory = Join-Path $InstallDirectory ('bin/' + $version)
        $null = New-Item -ItemType Directory -Force -Path $binaryDirectory
        foreach ($download in $downloads) {
            $destination = Join-Path $binaryDirectory ($download.name + '.exe')
            if (Test-Path -LiteralPath $destination) { Assert-WindowsArtifact -Path $destination -Sha256 $download.sha256 -Architecture $architecture }
            else { Move-Item -LiteralPath $download.path -Destination $destination }
        }
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls
        foreach ($download in $downloads) { if (Test-Path -LiteralPath $download.path) { Remove-Item -LiteralPath $download.path } }
    }
    $runnerBinary = Join-Path $binaryDirectory 'agentflow-runner.exe'
    $gateBinary = Join-Path $binaryDirectory 'agentflow-gate-mcp.exe'
    $config = [ordered]@{ ServerUrl = $ServerUrl; WorkspaceRoot = $WorkspaceRoot; CliDirectory = $CliDirectory; PathEntries = @($PathEntries); Name = $Name; Concurrency = $Concurrency; bootstrapToken = $BindCode; RunnerBinary = $runnerBinary; GateBinary = $gateBinary }
    [IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json), $utf8)
    # 保存通用脚本以便离线查看和以后停止，脚本本身不包含绑定凭据。
    if ($PSCommandPath) {
        $savedScript = Join-Path $InstallDirectory 'install.ps1'
        if ([IO.Path]::GetFullPath($PSCommandPath) -ine $savedScript) { [IO.File]::WriteAllBytes($savedScript, [IO.File]::ReadAllBytes($PSCommandPath)) }
    }
    if ($NoStart) { Write-Host "Windows Runner installed in $InstallDirectory. It has not been started."; return }
    $keys = @('PATH', 'AGENTFLOW_SERVER_URL', 'AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN', 'AGENTFLOW_RUNNER_STATE_PATH', 'AGENTFLOW_RUNNER_WORKSPACE', 'AGENTFLOW_GATE_MCP_BINARY', 'AGENTFLOW_MANAGED_CLI_ROOT')
    $saved = @{}
    foreach ($key in $keys) { $saved[$key] = @{ Exists = Test-Path -LiteralPath "Env:$key"; Value = [Environment]::GetEnvironmentVariable($key, 'Process') } }
    try {
        $directories = @($CliDirectory) + @($PathEntries) | Where-Object { $_ } | ForEach-Object { [IO.Path]::GetFullPath($_) }
        $env:PATH = (@($directories) + @($env:PATH)) -join [IO.Path]::PathSeparator
        $env:AGENTFLOW_SERVER_URL = $ServerUrl
        $env:AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN = $BindCode
        $env:AGENTFLOW_RUNNER_STATE_PATH = $statePath
        $env:AGENTFLOW_RUNNER_WORKSPACE = $WorkspaceRoot
        $env:AGENTFLOW_GATE_MCP_BINARY = $gateBinary
        Remove-Item -LiteralPath 'Env:AGENTFLOW_MANAGED_CLI_ROOT' -ErrorAction SilentlyContinue
        # 原生命令行的闭引号前需将尾部反斜杠加倍，防止名称吞掉后续参数。
        $quotedName = '"' + ($Name -replace '(\\+)$', '${1}${1}') + '"'
        $process = Start-Process -FilePath $runnerBinary -ArgumentList @('-auto-update=false', '-name', $quotedName, '-concurrency', [string]$Concurrency) -WorkingDirectory $InstallDirectory -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $InstallDirectory 'stdout.log') -RedirectStandardError (Join-Path $InstallDirectory 'stderr.log')
        [IO.File]::WriteAllText($pidPath, (@{ pid = $process.Id; startedTicks = $process.StartTime.ToUniversalTime().Ticks; binary = $runnerBinary } | ConvertTo-Json), $utf8)
        Start-Sleep -Seconds 2
        $process.Refresh()
        if ($process.HasExited) { throw 'Runner exited during startup. Check stderr.log in the installation directory and renew an expired binding code.' }
        Write-Host "Windows Runner started using the current Windows user environment. Logs: $InstallDirectory"
        Write-Host 'Select this execution machine in the platform and choose local authentication for Codex or Claude Code.'
    } finally {
        foreach ($key in $keys) {
            if ($saved[$key].Exists) { Set-Item -LiteralPath "Env:$key" -Value $saved[$key].Value }
            else { Remove-Item -LiteralPath "Env:$key" -ErrorAction SilentlyContinue }
        }
    }
} finally { $operationLock.Dispose() }
