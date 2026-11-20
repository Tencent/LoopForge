@echo off
setlocal enabledelayedexpansion

:: LoopForge Desktop Build Script for Windows
:: This script builds the complete desktop application

set SCRIPT_DIR=%~dp0
set PROJECT_ROOT=%~dp0..

:: Colors
set RED=^<nul set /p ".=^" ^> "%~2"
set GREEN=^<nul set /p ".=^" ^> "%~2"
set YELLOW=^<nul set /p ".=^" ^> "%~2"

:: Functions
:print_info
    echo [%~1] %~2
    exit /b

:print_error
    echo [%~1] %~2
    exit /b 1

:: Check if we're running from the correct directory
if not exist "%SCRIPT_DIR%package.json" (
    echo Error: Please run this script from the desktop-tauri directory
    pause
    exit /b 1
)

:: Print header
echo ========================================
echo LoopForge Desktop Build Script
echo ========================================
echo.

:: Step 1: Check prerequisites
call :print_info INFO "Checking prerequisites..."

where node >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error ERROR "Node.js is not installed or not in PATH"
)

where npm >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error ERROR "npm is not installed or not in PATH"
)

where rustc >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error ERROR "Rust is not installed or not in PATH"
)

where cargo >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error ERROR "Cargo is not installed or not in PATH"
)

where go >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error ERROR "Go is not installed or not in PATH"
)

:: Step 2: Install dependencies
call :print_info INFO "Installing Node.js dependencies..."
cd /d "%SCRIPT_DIR%"
if exist node_modules (
    echo Skipping npm install (node_modules already exists)
) else (
    npm install
    if %errorlevel% neq 0 (
        call :print_error ERROR "Failed to install dependencies"
    )
)

:: Step 3: Build Go backend
call :print_info INFO "Building Go backend..."
powershell -ExecutionPolicy Bypass -File "%SCRIPT_DIR%scripts\build-backend.ps1"
if %errorlevel% neq 0 (
    call :print_error ERROR "Failed to build Go backend"
)

:: Step 4: Build Tauri app
call :print_info INFO "Building Tauri application..."
cargo tauri build
if %errorlevel% neq 0 (
    call :print_error ERROR "Failed to build Tauri application"
)

:: Step 5: Success
call :print_info INFO "Build completed successfully!"
echo.
echo Output directory: %SCRIPT_DIR%src-tauri\target\release\bundle

endlocal
pause
