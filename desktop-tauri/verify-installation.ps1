#!/usr/bin/env powershell
<#
LoopForge Desktop Tauri - Installation Verification Script
This script verifies that all dependencies are installed and ready.
#>

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "LoopForge Desktop Tauri - Installation Verification" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""

# Track success/failure
$allPassed = $true
$checks = @()

Function Check-Command {
    param(
        [string]$Command,
        [string]$DisplayName,
        [string]$InstallUrl = ""
    )
    
    Write-Host "Checking: $DisplayName" -NoNewline
    
    try {
        if ($Command -eq "git") {
            $result = & git --version 2>$null
        } elseif ($Command -eq "node") {
            $result = & node --version 2>$null
        } elseif ($Command -eq "npm") {
            $result = & npm --version 2>$null
        } elseif ($Command -eq "rustc") {
            $result = & rustc --version 2>$null
        } elseif ($Command -eq "cargo") {
            $result = & cargo --version 2>$null
        } elseif ($Command -eq "go") {
            $result = & go version 2>$null
        } elseif ($Command -eq "docker") {
            $result = & docker --version 2>$null
        } else {
            $result = & $Command --version 2>$null
        }
        
        if ($result) {
            Write-Host " [✓ PASS]" -ForegroundColor Green
            $checks += @{ Name = $DisplayName; Status = "PASS"; Version = $result.Trim() }
            return $true
        }
    } catch {
        Write-Host " [✗ FAIL]" -ForegroundColor Red
        if ($InstallUrl) {
            Write-Host "  Install: $InstallUrl" -ForegroundColor Yellow
        }
        $checks += @{ Name = $DisplayName; Status = "FAIL"; Version = "Not found" }
        $script:allPassed = $false
        return $false
    }
    
    Write-Host " [✗ FAIL]" -ForegroundColor Red
    if ($InstallUrl) {
        Write-Host "  Install: $InstallUrl" -ForegroundColor Yellow
    }
    $checks += @{ Name = $DisplayName; Status = "FAIL"; Version = "Not found" }
    $script:allPassed = $false
    return $false
}

# Perform checks
Write-Host "Checking required dependencies..." -ForegroundColor Cyan
Write-Host ""

Check-Command -Command "git" -DisplayName "Git" -InstallUrl "https://git-scm.com/downloads"
Check-Command -Command "node" -DisplayName "Node.js" -InstallUrl "https://nodejs.org/"
Check-Command -Command "npm" -DisplayName "npm" -InstallUrl "https://nodejs.org/"
Check-Command -Command "rustc" -DisplayName "Rust Compiler" -InstallUrl "https://rustup.rs/"
Check-Command -Command "cargo" -DisplayName "Cargo (Rust Package Manager)" -InstallUrl "https://rustup.rs/"
Check-Command -Command "go" -DisplayName "Go" -InstallUrl "https://golang.org/dl/"
Check-Command -Command "docker" -DisplayName "Docker" -InstallUrl "https://www.docker.com/products/docker-desktop/"

Write-Host ""

# Check Node.js version (must be >= 16)
if ((Check-Command -Command "node" -DisplayName "Node.js")) {
    $nodeVersion = & node --version 2>$null
    $nodeMajor = $nodeVersion.Trim().Substring(1).Split('.')[0]
    if ($nodeMajor -ge 16) {
        Write-Host "  Node.js version check: $nodeVersion [✓ PASS]" -ForegroundColor Green
        $checks | Where-Object { $_.Name -eq "Node.js" } | ForEach-Object { $_.Version = "$nodeVersion (>= 16.0.0)" }
    } else {
        Write-Host "  Node.js version check: $nodeVersion [✗ FAIL - Requires >= 16.0.0]" -ForegroundColor Red
        $checks | Where-Object { $_.Name -eq "Node.js" } | ForEach-Object { $_.Status = "FAIL"; $_.Version = "$nodeVersion (Requires >= 16.0.0)" }
        $allPassed = $false
    }
}

Write-Host ""

# Check if we're in the right directory
$desktopTauriDir = Join-Path -Path $PSScriptRoot -ChildPath "desktop-tauri"
if (Test-Path -Path "desktop-tauri\package.json" -PathType Leaf) {
    Write-Host "Current directory: $($PWD.Path)" -ForegroundColor Green
    Write-Host "desktop-tauri found: [✓ PASS]" -ForegroundColor Green
    $checks += @{ Name = "LoopForge Desktop Tauri"; Status = "PASS"; Version = "Found" }
} else {
    Write-Host "desktop-tauri directory not found in: $($PSScriptRoot)" -ForegroundColor Red
    Write-Host "Please run this script from the LoopForge project root." -ForegroundColor Yellow
    $checks += @{ Name = "LoopForge Desktop Tauri"; Status = "FAIL"; Version = "Not found" }
    $allPassed = $false
}

Write-Host ""
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "Verification Results" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""

# Display summary
$passedCount = ($checks | Where-Object { $_.Status -eq "PASS" }).Count
$failedCount = ($checks | Where-Object { $_.Status -eq "FAIL" }).Count

foreach ($check in $checks) {
    $statusColor = if ($check.Status -eq "PASS") { "Green" } else { "Red" }
    Write-Host "[$($check.Status)] $($check.Name): $($check.Version)" -ForegroundColor $statusColor
}

Write-Host ""
Write-Host "Summary: $passedCount passed, $failedCount failed" -ForegroundColor White

if ($allPassed) {
    Write-Host ""
    Write-Host "✓ All dependencies are installed!" -ForegroundColor Green
    Write-Host ""
    Write-Host "To get started:" -ForegroundColor Cyan
    Write-Host "  1. cd desktop-tauri" -ForegroundColor White
    Write-Host "  2. npm install" -ForegroundColor White
    Write-Host "  3. npm run build:backend" -ForegroundColor White
    Write-Host "  4. npm run tauri dev" -ForegroundColor White
    Write-Host ""
    exit 0
} else {
    Write-Host ""
    Write-Host "✗ Some dependencies are missing or have version issues." -ForegroundColor Red
    Write-Host "Please install the missing dependencies and try again." -ForegroundColor Yellow
    Write-Host ""
    exit 1
}
