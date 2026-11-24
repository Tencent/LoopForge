#!/usr/bin/env powershell

<#
LoopForge Desktop Setup Script
This script sets up the development environment for the desktop app
#>

param(
    [switch]$Clean,
    [switch]$Force
)

$ScriptDir = $PSScriptRoot
$ProjectRoot = (Get-Item $ScriptDir).Parent.FullName

# Functions
function Write-Header([string]$message) {
    Write-Host "`n========================================" -ForegroundColor Cyan
    Write-Host $message -ForegroundColor Cyan
    Write-Host "========================================`n" -ForegroundColor Cyan
}

function Write-Info([string]$message) {
    Write-Host "[INFO] $message" -ForegroundColor Green
}

function Write-Warn([string]$message) {
    Write-Host "[WARN] $message" -ForegroundColor Yellow
}

function Write-Error([string]$message) {
    Write-Host "[ERROR] $message" -ForegroundColor Red
    exit 1
}

# Print header
Write-Header "LoopForge Desktop Setup"

# Check if we're running from the correct directory
if (-not (Test-Path (Join-Path $ScriptDir "package.json"))) {
    Write-Error "Please run this script from the desktop-tauri directory"
}

# Check prerequisites
Write-Info "Checking prerequisites..."

$Required = @{
    "Node.js (18+)" = "node";
    "npm" = "npm";
    "Rust (1.70+)" = "rustc";
    "Cargo" = "cargo";
    "Go (1.26+)" = "go";
    "Tauri CLI" = "tauri"
}

$Missing = @()
foreach ($req in $Required.GetEnumerator()) {
    if (-not (Get-Command $req.Value -ErrorAction SilentlyContinue)) {
        $Missing += $req.Key
    }
}

if ($Missing.Count -gt 0) {
    Write-Warn "Missing prerequisites: $($Missing -join ', ')"
    
    if (-not $Force) {
        Write-Error "Please install the missing prerequisites first"
    }
}

# Check versions
$NodeVersion = (node --version).Trim()
if ($NodeVersion -and $NodeVersion -notmatch "v1[8-9]\.|v2[0-9]\.") {
    Write-Warn "Node.js version $NodeVersion may be too old (recommend 18+)"
}

$RustVersion = (rustc --version).Trim()
if ($RustVersion -and $RustVersion -notmatch "rustc 1\.[7-9][0-9]\.") {
    Write-Warn "Rust version $RustVersion may be too old (recommend 1.70+)"
}

$GoVersion = (go version).Trim()
if ($GoVersion -and $GoVersion -notmatch "go1\.2[6-9]|go1\.[3-9][0-9]") {
    Write-Warn "Go version $GoVersion may be too old (recommend 1.26+)"
}

# Clean (if requested)
if ($Clean) {
    Write-Info "Cleaning..."
    if (Test-Path "node_modules") { Remove-Item "node_modules" -Recurse -Force }
    if (Test-Path "dist") { Remove-Item "dist" -Recurse -Force }
    if (Test-Path "src-tauri/target") { Remove-Item "src-tauri/target" -Recurse -Force }
    if (Test-Path "bin") { Remove-Item "bin" -Recurse -Force }
    Write-Info "Clean completed"
}

# Install Node.js dependencies
Write-Info "Installing Node.js dependencies..."
if (-not (Test-Path "node_modules") -or $Force) {
    npm install
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to install Node.js dependencies"
    }
} else {
    Write-Info "Node.js dependencies already installed"
}

# Install Rust dependencies
Write-Info "Installing Rust dependencies..."
cd src-tauri
cargo tauri deps
if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to install Rust dependencies"
}
cd ..

# Build Go backend
Write-Info "Building Go backend..."
& "$ScriptDir/scripts/build-backend.ps1"
if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to build Go backend"
}

# Verify setup
Write-Info "Verifying setup..."

$Checks = @(
    "node_modules",
    "src-tauri/target",
    "src-tauri/bin/loopforge-backend.exe",
    "src-tauri/loopforge-backend.exe"
)

$Passed = 0
$Failed = 0

foreach ($check in $Checks) {
    if (Test-Path $check) {
        Write-Info "✓ $check exists"
        $Passed++
    } else {
        Write-Warn "✗ $check not found"
        $Failed++
    }
}

# Summary
Write-Info "`nSetup Summary:"
Write-Info "  Passed: $Passed checks"
if ($Failed -gt 0) {
    Write-Warn "  Failed: $Failed checks"
}

if ($Failed -eq 0) {
    Write-Info "`n✅ Setup completed successfully!"
    Write-Info ""
    Write-Info "To start development:"
    Write-Info "  cd $ScriptDir"
    Write-Info "  TAURI_ENV_DEV=true npm run tauri dev"
    Write-Info ""
    Write-Info "To build for production:"
    Write-Info "  cd $ScriptDir"
    Write-Info "  npm run tauri build"
} else {
    Write-Warn "`n⚠️  Setup completed with warnings"
    Write-Warn "Some checks failed, but you may still be able to run the app"
}
