#!/usr/bin/env powershell
<#
LoopForge Desktop Tauri - Application Test Suite
This script tests various aspects of the Tauri application.
#>

param(
    [switch]$SkipBuild,
    [switch]$QuickTest
)

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "LoopForge Desktop Tauri - Test Suite" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""

# Configuration
$projectRoot = $PSScriptRoot
$desktopTauriDir = Join-Path -Path $projectRoot -ChildPath "desktop-tauri"
$backendDir = Join-Path -Path $projectRoot -ChildPath "loopforge-backend"

# Track test results
$testsPassed = 0
$testsFailed = 0
$testResults = @()

Function Run-Test {
    param(
        [string]$Name,
        [scriptblock]$TestScript
    )
    
    Write-Host "Testing: $Name" -NoNewline
    
    try {
        $result = & $TestScript
        if ($result -eq $true) {
            Write-Host " [✓ PASS]" -ForegroundColor Green
            $script:testsPassed++
            $script:testResults += @{ Name = $Name; Status = "PASS" }
        } else {
            Write-Host " [✗ FAIL]" -ForegroundColor Red
            $script:testsFailed++
            $script:testResults += @{ Name = $Name; Status = "FAIL" }
        }
    } catch {
        Write-Host " [✗ ERROR] $($_.Exception.Message)" -ForegroundColor Red
        $script:testsFailed++
        $script:testResults += @{ Name = $Name; Status = "ERROR"; Message = $_.Exception.Message }
    }
}

Function Write-TestHeader {
    param([string]$Section)
    Write-Host ""
    Write-Host "----------------------------------------" -ForegroundColor Cyan
    Write-Host "$Section" -ForegroundColor Cyan
    Write-Host "----------------------------------------" -ForegroundColor Cyan
}

# Check if we're in the right place
if (-not (Test-Path -Path "desktop-tauri\package.json")) {
    Write-Host "✗ Error: Please run this script from the LoopForge project root." -ForegroundColor Red
    exit 1
}

# Test 1: File Structure
Write-TestHeader -Section "File Structure Tests"

Run-Test -Name "package.json exists" {
    return (Test-Path -Path "desktop-tauri\package.json" -PathType Leaf)
}

Run-Test -Name "Cargo.toml exists" {
    return (Test-Path -Path "desktop-tauri\src-tauri\Cargo.toml" -PathType Leaf)
}

Run-Test -Name "tauri.conf.json exists" {
    return (Test-Path -Path "desktop-tauri\src-tauri\tauri.conf.json" -PathType Leaf)
}

Run-Test -Name "main.rs exists" {
    return (Test-Path -Path "desktop-tauri\src-tauri\src\main.rs" -PathType Leaf)
}

Run-Test -Name "index.html exists" {
    return (Test-Path -Path "desktop-tauri\src\index.html" -PathType Leaf)
}

Run-Test -Name "tauri-compat.js exists" {
    return (Test-Path -Path "desktop-tauri\src\tauri-compat.js" -PathType Leaf)
}

Run-Test -Name "tauri-interface.js exists" {
    return (Test-Path -Path "desktop-tauri\src\tauri-interface.js" -PathType Leaf)
}

Run-Test -Name "All icons exist" {
    $icons = @("32x32", "48x48", "64x64", "128x128", "256x256", "512x512")
    foreach ($icon in $icons) {
        if (-not (Test-Path -Path "desktop-tauri\src-tauri\icons\icon-${icon}.ico")) {
            return $false
        }
    }
    return $true
}

# Test 2: Configuration Files
Write-TestHeader -Section "Configuration Tests"

Run-Test -Name "package.json has required scripts" {
    $packageJson = Get-Content -Path "desktop-tauri\package.json" -Raw | ConvertFrom-Json
    $requiredScripts = @("dev", "tauri dev", "build", "tauri build", "build:backend")
    foreach ($script in $requiredScripts) {
        if (-not ($packageJson.scripts.PSObject.Properties.Name -contains $script)) {
            Write-Host "  Missing script: $script" -ForegroundColor Yellow
            return $false
        }
    }
    return $true
}

Run-Test -Name "tauri.conf.json has correct package identifier" {
    $tauriConfig = Get-Content -Path "desktop-tauri\src-tauri\tauri.conf.json" -Raw | ConvertFrom-Json
    return ($tauriConfig.package.productName -eq "LoopForge Desktop")
}

Run-Test -Name "tauri.conf.json has Windows config" {
    $tauriConfig = Get-Content -Path "desktop-tauri\src-tauri\tauri.conf.json" -Raw | ConvertFrom-Json
    return ($tauriConfig.bundle.windows -ne $null)
}

Run-Test -Name "Cargo.toml has tauri dependencies" {
    $cargoToml = Get-Content -Path "desktop-tauri\src-tauri\Cargo.toml" -Raw
    return ($cargoToml -match "tauri" -and $cargoToml -match "tauri-build")
}

# Test 3: Rust Code
Write-TestHeader -Section "Rust Code Tests"

Run-Test -Name "main.rs has start_backend command" {
    $mainRs = Get-Content -Path "desktop-tauri\src-tauri\src\main.rs" -Raw
    return ($mainRs -match "start_backend")
}

Run-Test -Name "main.rs has show_notification command" {
    $mainRs = Get-Content -Path "desktop-tauri\src-tauri\src\main.rs" -Raw
    return ($mainRs -match "show_notification")
}

Run-Test -Name "main.rs has check_docker_running command" {
    $mainRs = Get-Content -Path "desktop-tauri\src-tauri\src\main.rs" -Raw
    return ($mainRs -match "check_docker_running")
}

# Test 4: JavaScript Compatibility Layer
Write-TestHeader -Section "JavaScript Compatibility Tests"

Run-Test -Name "tauri-compat.js has fetch override" {
    $compatJs = Get-Content -Path "desktop-tauri\src\tauri-compat.js" -Raw
    return ($compatJs -match "fetch")
}

Run-Test -Name "tauri-compat.js has EventSource override" {
    $compatJs = Get-Content -Path "desktop-tauri\src\tauri-compat.js" -Raw
    return ($compatJs -match "EventSource")
}

Run-Test -Name "tauri-interface.js has Tauri commands" {
    $interfaceJs = Get-Content -Path "desktop-tauri\src\tauri-interface.js" -Raw
    $requiredCommands = @("start_backend", "stop_backend", "show_notification", "check_docker_running")
    foreach ($cmd in $requiredCommands) {
        if (-not ($interfaceJs -match $cmd)) {
            Write-Host "  Missing command: $cmd" -ForegroundColor Yellow
            return $false
        }
    }
    return $true
}

# Test 5: Build Process (if not skipped)
if (-not $SkipBuild) {
    Write-TestHeader -Section "Build Process Tests"
    
    # Check if Rust toolchain is available
    Run-Test -Name "Rust toolchain available" {
        return ((& rustc --version 2>$null) -and (& cargo --version 2>$null))
    }
    
    # Check if Node.js is available
    Run-Test -Name "Node.js available" {
        return (& node --version 2>$null)
    }
    
    # Check if npm is available
    Run-Test -Name "npm available" {
        return (& npm --version 2>$null)
    }
    
    # Check if Go is available
    Run-Test -Name "Go available" {
        return (& go version 2>$null)
    }
    
    # Check if Docker is available
    Run-Test -Name "Docker available" {
        return (& docker --version 2>$null)
    }
    
    # Only run actual build tests if QuickTest is not set
    if (-not $QuickTest) {
        Write-Host ""
        Write-Host "Running npm install..." -ForegroundColor Cyan
        Push-Location -Path "desktop-tauri"
        try {
            $installOutput = & npm install 2>&1 | Out-String
            Run-Test -Name "npm install succeeds" {
                return $true
            }
        } catch {
            Run-Test -Name "npm install succeeds" {
                return $false
            }
        } finally {
            Pop-Location
        }
    }
}

# Display Summary
Write-Host ""
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "Test Results Summary" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""

$totalTests = $testsPassed + $testsFailed
Write-Host "Total tests: $totalTests" -ForegroundColor White
Write-Host "Passed: $testsPassed" -ForegroundColor Green
Write-Host "Failed: $testsFailed" -ForegroundColor Red
Write-Host ""

if ($testResults.Count -gt 0) {
    Write-Host "Detailed Results:" -ForegroundColor Cyan
    foreach ($result in $testResults) {
        $color = if ($result.Status -eq "PASS") { "Green" } else { "Red" }
        Write-Host "  [$($result.Status)] $($result.Name)" -ForegroundColor $color
        if ($result.Message) {
            Write-Host "      $($result.Message)" -ForegroundColor Yellow
        }
    }
}

Write-Host ""

if ($testsFailed -eq 0) {
    Write-Host "✓ All tests passed!" -ForegroundColor Green
    Write-Host ""
    if ($SkipBuild -or $QuickTest) {
        Write-Host "Next steps:" -ForegroundColor Cyan
        Write-Host "  1. cd desktop-tauri" -ForegroundColor White
        Write-Host "  2. npm install" -ForegroundColor White
        Write-Host "  3. npm run build:backend" -ForegroundColor White
        Write-Host "  4. npm run tauri dev" -ForegroundColor White
    } else {
        Write-Host "Build and run:" -ForegroundColor Cyan
        Write-Host "  cd desktop-tauri" -ForegroundColor White
        Write-Host "  npm run tauri dev" -ForegroundColor White
    }
    exit 0
} else {
    Write-Host "✗ Some tests failed." -ForegroundColor Red
    Write-Host "Please fix the issues above and try again." -ForegroundColor Yellow
    exit 1
}
