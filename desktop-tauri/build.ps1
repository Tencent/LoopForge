#!/usr/bin/env powershell

<#
LoopForge Desktop Build Script for Windows
This script builds the complete desktop application
#>

param(
    [switch]$Clean,
    [switch]$Dev,
    [switch]$SkipBackend
)

$ScriptDir = $PSScriptRoot
$ProjectRoot = (Get-Item $ScriptDir).Parent.FullName

# Functions
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
Write-Info "========================================"
Write-Info "LoopForge Desktop Build Script"
Write-Info "========================================"
Write-Info ""

# Check if we're running from the correct directory
if (-not (Test-Path (Join-Path $ScriptDir "package.json"))) {
    Write-Error "Please run this script from the desktop-tauri directory"
}

# Step 1: Clean (if requested)
if ($Clean) {
    Write-Info "Cleaning..."
    if (Test-Path "dist") { Remove-Item "dist" -Recurse -Force }
    if (Test-Path "node_modules") { Remove-Item "node_modules" -Recurse -Force }
    if (Test-Path "src-tauri/target") { Remove-Item "src-tauri/target" -Recurse -Force }
    if (Test-Path "bin") { Remove-Item "bin" -Recurse -Force }
    Write-Info "Clean completed"
}

# Step 2: Check prerequisites
Write-Info "Checking prerequisites..."

$Prerequisites = @{
    "Node.js" = "node";
    "npm" = "npm";
    "Rust" = "rustc";
    "Cargo" = "cargo";
    "Go" = "go"
}

$Missing = @()
foreach ($req in $Prerequisites.GetEnumerator()) {
    if (-not (Get-Command $req.Value -ErrorAction SilentlyContinue)) {
        $Missing += $req.Key
    }
}

if ($Missing.Count -gt 0) {
    Write-Error "Missing prerequisites: $($Missing -join ', ')"
}

# Step 3: Install dependencies
Write-Info "Installing Node.js dependencies..."
Set-Location $ScriptDir
if (-not (Test-Path "node_modules")) {
    npm install
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to install dependencies"
    }
} else {
    Write-Info "Skipping npm install (node_modules already exists)"
}

# Step 4: Build Go backend
if (-not $SkipBackend) {
    Write-Info "Building Go backend..."
    & "$ScriptDir/scripts/build-backend.ps1" -OutputDir "$ScriptDir/bin"
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build Go backend"
    }
} else {
    Write-Info "Skipping backend build (--SkipBackend)"
}

# Step 5: Build Tauri app
Write-Info "Building Tauri application..."
if ($Dev) {
    Write-Info "Running in development mode"
    $env:TAURI_ENV_DEV = "true"
    cargo tauri dev
} else {
    Write-Info "Running in production mode"
    cargo tauri build
}

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to build Tauri application"
}

# Step 6: Success
Write-Info ""
Write-Info "========================================"
Write-Info "Build completed successfully!"
Write-Info "========================================"
Write-Info ""

if ($Dev) {
    Write-Info "Tauri dev server is running..."
} else {
    $OutputDir = "$ScriptDir/src-tauri/target/release/bundle"
    Write-Info "Output directory: $OutputDir"
    
    # List outputs
    if (Test-Path "$OutputDir/msi") {
        Write-Info "MSI installer: $OutputDir/msi/"
    }
    if (Test-Path "$OutputDir/nsis") {
        Write-Info "NSIS installer: $OutputDir/nsis/"
    }
}
