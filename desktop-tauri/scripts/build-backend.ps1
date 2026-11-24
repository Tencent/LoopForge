# Build script for the Go backend on Windows
# This script builds the Go backend for Windows

param(
    [string]$OutputDir = "$(Join-Path $PSScriptRoot "..\..\desktop-tauri\bin")",
    [string]$GOOS = "windows",
    [string]$GOARCH = "amd64"
)

# Ensure output directory exists
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

$ProjectRoot = (Get-Item $PSScriptRoot).Parent.Parent.FullName
$OutputBinary = Join-Path $OutputDir "loopforge-backend.exe"

Write-Host "[INFO] Building Go backend..." -ForegroundColor Green
Write-Host "[INFO] Project root: $ProjectRoot" -ForegroundColor Green
Write-Host "[INFO] Output: $OutputBinary" -ForegroundColor Green

# Check if Go is installed
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[ERROR] Go is not installed. Please install Go 1.26+ first." -ForegroundColor Red
    exit 1
}

$GoVersion = go version
Write-Host "[INFO] Go version: $GoVersion" -ForegroundColor Green

# Build the backend
$BuildArgs = @(
    "build",
    "-o", $OutputBinary,
    "-trimpath",
    "-ldflags", "-s -w",
    "-C", $ProjectRoot
)

if ($GOOS -ne "windows") {
    $BuildArgs += @("-GOOS=$GOOS")
}

if ($GOARCH -ne "amd64") {
    $BuildArgs += @("-GOARCH=$GOARCH")
}

# Set environment for cross-compilation
$env:GOOS = $GOOS
$env:GOARCH = $GOARCH
$env:CGO_ENABLED = 0

try {
    Write-Host "[INFO] Building agentflow server for $GOOS/$GOARCH..." -ForegroundColor Green
    
    # Change to the agentflow directory
    $AgentFlowDir = Join-Path $ProjectRoot "cmd\agentflow"
    if (-not (Test-Path $AgentFlowDir)) {
        Write-Host "[ERROR] Could not find cmd\agentflow directory" -ForegroundColor Red
        exit 1
    }
    
    Set-Location $AgentFlowDir
    
    # Build
    go $BuildArgs
    
    if (-not (Test-Path $OutputBinary)) {
        Write-Host "[ERROR] Build failed - output binary not created" -ForegroundColor Red
        exit 1
    }
    
    Write-Host "[INFO] Build completed successfully!" -ForegroundColor Green
    
    # Copy to desktop-tauri directory
    $TauriDir = Join-Path $ProjectRoot "desktop-tauri\src-tauri"
    Copy-Item $OutputBinary $TauriDir -Force
    Write-Host "[INFO] Copied binary to $TauriDir" -ForegroundColor Green
    
    # Also copy without extension for compatibility
    $OutputBinaryNoExt = Join-Path $OutputDir "loopforge-backend"
    Copy-Item $OutputBinary $OutputBinaryNoExt -Force
    
    Write-Host "" -ForegroundColor Green
    Write-Host "[SUCCESS] Go backend built successfully!" -ForegroundColor Green
    Write-Host "[INFO] Binary location: $OutputBinary" -ForegroundColor Green
    
} catch {
    Write-Host "[ERROR] Build failed: $_" -ForegroundColor Red
    exit 1
}
