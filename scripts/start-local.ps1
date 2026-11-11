<#
.SYNOPSIS
Starts a checkout-local LoopForge instance with Docker Desktop Linux containers.
.DESCRIPTION
Saves ports, project name and generated credentials in .local/windows. Applies
the published observability migration and public catalog through UTF-8 files.
.EXAMPLE
.\scripts\start-local.ps1 -HttpPort 18090 -MySqlPort 43315 -NoBrowser
.EXAMPLE
.\scripts\start-local.ps1 -Stop
#>
[CmdletBinding()]
param(
    [ValidatePattern('^[a-z0-9][a-z0-9_-]*$')][string]$ProjectName,
    [ValidateRange(1, 65535)][int]$HttpPort = 8090,
    [ValidateRange(1, 65535)][int]$MySqlPort = 33315,
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._/:@-]*$')][string]$Image,
    [string]$DockerBinary,
    [switch]$Stop,
    [switch]$NoBrowser
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$localDirectory = Join-Path $projectRoot '.local/windows'
$settingsFile = Join-Path $localDirectory 'settings.json'
$environmentFile = Join-Path $localDirectory '.env'
$overrideFile = Join-Path $localDirectory 'compose.yaml'
$utf8 = New-Object Text.UTF8Encoding($false)
$inheritedEnvironment = @{}

# Docker writes ordinary progress to stderr; only its exit status is authoritative.
function Invoke-LocalDocker {
    param([string[]]$Arguments, [switch]$CaptureOutput)
    $previousEncoding = $OutputEncoding
    $previousErrorAction = $ErrorActionPreference
    try {
        $OutputEncoding = $script:utf8
        $ErrorActionPreference = 'Continue'
        $output = @(& $script:DockerBinary @Arguments 2>&1)
        if ($LASTEXITCODE -ne 0) { throw ('Docker failed: ' + ($output -join [Environment]::NewLine)) }
        if ($CaptureOutput) { return ($output -join [Environment]::NewLine).Trim() }
        $output | ForEach-Object { Write-Host $_ }
    } finally {
        $OutputEncoding = $previousEncoding
        $ErrorActionPreference = $previousErrorAction
    }
}

# Hex avoids dotenv quoting and DSN interpolation ambiguities.
function New-LocalSecret {
    param([int]$ByteCount = 24)
    $bytes = New-Object byte[] $ByteCount
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}

# Transfer exact UTF-8 bytes instead of the PowerShell 5.1 native text pipeline.
function Invoke-LocalSql {
    param([string]$Sql)
    $containerID = Invoke-LocalDocker -Arguments ($script:composeArguments + @('ps', '--quiet', 'mysql')) -CaptureOutput
    if ($containerID -notmatch '^[a-f0-9]{12,64}$') { throw 'Cannot identify this instance MySQL container.' }
    $fileName = 'loopforge-windows-' + [Guid]::NewGuid().ToString('N') + '.sql'
    $hostFile = Join-Path ([IO.Path]::GetTempPath()) $fileName
    $containerFile = '/tmp/' + $fileName
    try {
        [IO.File]::WriteAllText($hostFile, $Sql, $script:utf8)
        $null = Invoke-LocalDocker -Arguments @('cp', $hostFile, ($containerID + ':' + $containerFile)) -CaptureOutput
        $arguments = $script:composeArguments + @('exec', '-T', 'mysql', 'sh', '-c', 'MYSQL_PWD="$MYSQL_PASSWORD" mysql --default-character-set=utf8mb4 --batch --skip-column-names -u loopforge loopforge < "$1"', 'loopforge-sql', $containerFile)
        return Invoke-LocalDocker -Arguments $arguments -CaptureOutput
    } finally {
        if (Test-Path -LiteralPath $hostFile) { Remove-Item -LiteralPath $hostFile }
        try { $null = Invoke-LocalDocker -Arguments @('exec', $containerID, 'rm', '-f', '--', $containerFile) -CaptureOutput }
        catch { Write-Warning 'Could not remove the temporary SQL file in this instance container.' }
    }
}

# This explicit installer step follows scripts/install-observability-schema.sh.
function Initialize-LocalDatabase {
    $sample = [string][char]0x4E2D + [char]0x6587 + [char]0x6D4B + [char]0x8BD5
    $expected = ([BitConverter]::ToString($script:utf8.GetBytes($sample))).Replace('-', '')
    $received = Invoke-LocalSql -Sql ("SET NAMES utf8mb4; SELECT HEX('" + $sample + "');")
    if ($received -cne $expected) { throw 'MySQL UTF-8 round-trip failed; catalog import has been stopped.' }
    $migration = [IO.File]::ReadAllText((Join-Path $script:projectRoot 'migrations/001_run_obs_generated_columns.sql'), $script:utf8)
    foreach ($entry in @(@('run_telemetry', 'adapter'), @('run_logs', 'level'))) {
        $table = $entry[0]
        $column = $entry[1]
        $count = Invoke-LocalSql -Sql "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='$table' AND column_name='$column';"
        if ($count -eq '1') { continue }
        if ($count -ne '0') { throw "Cannot inspect migration state for $table.$column." }
        $definition = [regex]::Match($migration, '(?s)ALTER TABLE ' + [regex]::Escape($table) + '\b.*?;')
        if (-not $definition.Success) { throw "Missing upstream migration for $table.$column." }
        $null = Invoke-LocalSql -Sql $definition.Value
    }
    foreach ($seedName in @('001_public_catalog.sql', '002_welcome_project.sql')) {
        $seedPath = Join-Path $script:projectRoot ('migrations/seed/' + $seedName)
        $null = Invoke-LocalSql -Sql ([IO.File]::ReadAllText($seedPath, $script:utf8))
    }
    Write-Host 'UTF-8 verified; observability columns and public catalog installed.'
}

try {
    if (-not $DockerBinary) {
        $dockerCommand = Get-Command docker -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($dockerCommand) { $DockerBinary = $dockerCommand.Source }
    }
    if (-not $DockerBinary -or -not (Test-Path -LiteralPath $DockerBinary -PathType Leaf)) {
        throw 'Install Docker Desktop, enable Linux containers and add docker to PATH (or pass -DockerBinary).'
    }
    if (Test-Path -LiteralPath $settingsFile) {
        $settings = [IO.File]::ReadAllText($settingsFile, $utf8) | ConvertFrom-Json
        foreach ($entry in @(@('ProjectName', 'projectName'), @('HttpPort', 'httpPort'), @('MySqlPort', 'mySqlPort'))) {
            if ($PSBoundParameters.ContainsKey($entry[0]) -and (Get-Variable -Name $entry[0] -ValueOnly) -ne $settings.($entry[1])) {
                throw "Saved $($entry[0]) differs. Use the recorded settings in $settingsFile, or another checkout for a separate instance."
            }
        }
        $ProjectName = $settings.projectName
        $HttpPort = $settings.httpPort
        $MySqlPort = $settings.mySqlPort
    } elseif ($Stop) {
        Write-Host 'No saved Windows instance in this checkout.'
        return
    }
    if (-not $ProjectName) {
        $hash = [Security.Cryptography.SHA256]::Create()
        try { $suffix = ([BitConverter]::ToString($hash.ComputeHash($utf8.GetBytes($projectRoot.ToLowerInvariant())))).Replace('-', '').Substring(0, 8).ToLowerInvariant() }
        finally { $hash.Dispose() }
        $ProjectName = 'loopforge-windows-' + $suffix
    }
    if ($ProjectName -notmatch '^[a-z0-9][a-z0-9_-]*$') { throw 'Invalid saved Compose project name.' }
    if ($HttpPort -lt 1 -or $HttpPort -gt 65535 -or $MySqlPort -lt 1 -or $MySqlPort -gt 65535) { throw 'Saved ports must be between 1 and 65535.' }
    if ($HttpPort -eq $MySqlPort) { throw 'HTTP and MySQL ports must differ.' }
    if ($Stop) { & (Join-Path $PSScriptRoot 'start-local-runner.ps1') -Stop }
    $engineType = Invoke-LocalDocker -Arguments @('info', '--format', '{{.OSType}}') -CaptureOutput
    if ($engineType -ne 'linux') { throw 'Start Docker Desktop and select Linux containers.' }
    $composeVersion = Invoke-LocalDocker -Arguments @('compose', 'version', '--short') -CaptureOutput
    if ($composeVersion -notmatch '(\d+\.\d+\.\d+)' -or [version]$Matches[1] -lt [version]'2.24.4') {
        throw 'Docker Compose 2.24.4 or newer is required for !override.'
    }
    $workspaceVolume = $ProjectName + '-workspaces'
    $composeArguments = @('compose', '--project-name', $ProjectName, '--env-file', $environmentFile, '-f', (Join-Path $projectRoot 'compose.yaml'), '-f', $overrideFile, '--profile', 'platform')
    if (-not $Stop) {
        $null = New-Item -ItemType Directory -Path $localDirectory -Force
        $null = Invoke-LocalDocker -Arguments @('volume', 'create', '--label', ('com.loopforge.windows-project=' + $ProjectName), $workspaceVolume) -CaptureOutput
        $mountPoint = Invoke-LocalDocker -Arguments @('volume', 'inspect', '--format', '{{.Mountpoint}}', $workspaceVolume) -CaptureOutput
        if ($mountPoint -notmatch '^/[A-Za-z0-9_./-]+$' -or $mountPoint -match '(^|/)\.\.(/|$)') { throw 'Docker did not return a safe Linux volume path.' }
        $settings = @{ projectName = $ProjectName; httpPort = $HttpPort; mySqlPort = $MySqlPort }
        [IO.File]::WriteAllText($settingsFile, ($settings | ConvertTo-Json), $utf8)
        if (-not (Test-Path -LiteralPath $environmentFile)) {
            $values = [ordered]@{
                COMPOSE_PROJECT_NAME = $ProjectName
                LOOPFORGE_BIND = '127.0.0.1'
                AGENTFLOW_HTTP_PORT = $HttpPort
                LOOPFORGE_MYSQL_PORT = $MySqlPort
                LOOPFORGE_DATA_DIR = $mountPoint
                LOOPFORGE_DB_PASSWORD = New-LocalSecret
                LOOPFORGE_DB_ROOT_PASSWORD = New-LocalSecret
                AGENTFLOW_SESSION_SECRET = New-LocalSecret -ByteCount 32
                AGENTFLOW_ALLOW_SIGNUP = 'false'
                AGENTFLOW_SECURE_COOKIES = 'false'
                AGENTFLOW_GITHUB_CLIENT_ID = ''
                AGENTFLOW_ASSISTANT_MODEL = ''
                AGENTFLOW_ASSISTANT_MODELS = ''
                AGENTFLOW_PREVIEW_PUBLIC_URL = ''
                AGENTFLOW_PLATFORM_MCP_URL = 'http://platform:8080'
                LOOPFORGE_DOCKER_SOCKET = '/var/run/docker.sock'
                LOOPFORGE_IMAGE = $Image
            }
            $lines = foreach ($key in $values.Keys) { "$key=$($values[$key])" }
            [IO.File]::WriteAllText($environmentFile, ($lines -join "`n") + "`n", $utf8)
        } elseif ($Image) {
            $envText = [IO.File]::ReadAllText($environmentFile, $utf8)
            $envText = [regex]::Replace($envText, '(?m)^LOOPFORGE_IMAGE=.*$', ('LOOPFORGE_IMAGE=' + $Image))
            [IO.File]::WriteAllText($environmentFile, $envText, $utf8)
        }
        $override = @"
services:
  mysql:
    restart: unless-stopped
  platform:
    volumes: !override
      - type: volume
        source: platform-data
        target: /data
      - type: volume
        source: windows-workspaces
        target: '$mountPoint'
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
volumes:
  windows-workspaces:
    external: true
    name: $workspaceVolume
"@
        [IO.File]::WriteAllText($overrideFile, $override + "`n", $utf8)
    }
    if (-not (Test-Path -LiteralPath $environmentFile) -or -not (Test-Path -LiteralPath $overrideFile)) { throw 'Saved Windows instance configuration is incomplete.' }
    $recordedValues = @{}
    foreach ($line in [IO.File]::ReadAllLines($environmentFile, $utf8)) {
        if ($line -match '^([A-Z][A-Z0-9_]*)=(.*)$') { $recordedValues[$Matches[1]] = $Matches[2] }
    }
    $expectedValues = @{ COMPOSE_PROJECT_NAME = $ProjectName; AGENTFLOW_HTTP_PORT = [string]$HttpPort; LOOPFORGE_MYSQL_PORT = [string]$MySqlPort }
    if (-not $Stop) { $expectedValues['LOOPFORGE_DATA_DIR'] = $mountPoint }
    foreach ($key in $expectedValues.Keys) {
        if ($recordedValues[$key] -ne $expectedValues[$key]) { throw "The saved .env $key differs from this instance settings. Restore the recorded value before starting." }
    }
    # Explicit instance files take precedence over values inherited from other checkouts.
    foreach ($line in [IO.File]::ReadAllLines($environmentFile, $utf8)) {
        if ($line -match '^([A-Z][A-Z0-9_]*)=') {
            $key = $Matches[1]
            $inheritedEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
            [Environment]::SetEnvironmentVariable($key, $null, 'Process')
        }
    }
    if ($Stop) {
        Invoke-LocalDocker -Arguments ($composeArguments + @('stop'))
        Write-Host 'Instance stopped. Configuration, database and workspaces retained.'
        return
    }
    Invoke-LocalDocker -Arguments ($composeArguments + @('up', '-d', '--wait', '--wait-timeout', '300'))
    Initialize-LocalDatabase
    $workbenchUrl = 'http://127.0.0.1:' + $HttpPort
    Write-Host "LoopForge is ready at $workbenchUrl. Create the first administrator in the browser."
    Write-Host "Saved instance settings: $settingsFile"
    if (-not $NoBrowser) { Start-Process $workbenchUrl }
} catch {
    Write-Error ($_.Exception.Message + ' Configuration and data have been retained.')
    exit 1
} finally {
    foreach ($key in $inheritedEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key, $inheritedEnvironment[$key], 'Process') }
}
