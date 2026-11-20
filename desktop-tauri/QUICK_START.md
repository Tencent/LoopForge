# LoopForge Desktop - Quick Start Guide

## 🚀 Get Started in 5 Minutes

This guide will have you running LoopForge as a native Windows desktop app quickly.

---

## ⚡ Prerequisites

Before you begin, ensure you have the following installed:

| Dependency | Required | Version | Download |
|-----------|----------|---------|----------|
| Git | ✅ Yes | Latest | [git-scm.com](https://git-scm.com/downloads) |
| Node.js | ✅ Yes | >= 16.0.0 | [nodejs.org](https://nodejs.org/) |
| Rust | ✅ Yes | Latest | [rustup.rs](https://rustup.rs/) |
| Go | ✅ Yes | >= 1.20 | [golang.org/dl](https://golang.org/dl/) |
| Docker | ✅ Yes | Latest | [docker.com](https://www.docker.com/products/docker-desktop/) |

### Verify Installation

Run the verification script to check all dependencies:

```powershell
cd desktop-tauri
..\desktop-tauri\verify-installation.ps1
```

Or manually check:

```powershell
# Check all required tools
git --version
node --version
npm --version
rustc --version
cargo --version
go version
docker --version
```

---

## 📥 Step 1: Install Dependencies

From the **LoopForge project root**:

```powershell
# Navigate to desktop-tauri directory
cd desktop-tauri

# Install Node.js dependencies (Tauri CLI, Vite, etc.)
npm install
```

This will install:
- Tauri CLI (`tauri`, `cargo-tauri`)
- Vite (for frontend development)
- All required npm packages

---

## 🔧 Step 2: Build the Go Backend

The Tauri app requires the LoopForge Go backend to be built first.

```powershell
# From desktop-tauri directory
npm run build:backend
```

This script will:
1. Navigate to the LoopForge backend directory
2. Build the Go binary (`loopforge-backend.exe` on Windows)
3. Copy it to the Tauri resources directory

**Output location**: `desktop-tauri\src-tauri\resources\loopforge-backend.exe`

---

## 🏃 Step 3: Run in Development Mode

### Option A: Full Development (Backend Auto-Starts)

```powershell
# From desktop-tauri directory
npm run tauri dev
```

This will:
1. Start the Tauri application
2. Automatically launch the Go backend
3. Open the desktop app window
4. Proxy requests to the backend

### Option B: Manual Backend (For Testing)

If you want to manually control the backend:

```powershell
# Terminal 1: Start Go backend manually
cd ..
cd loopforge-backend
loopforge-backend.exe

# Terminal 2: Start Tauri with dev flag
cd desktop-tauri
TAURI_ENV_DEV=true npm run tauri dev
```

---

## 📦 Step 4: Build for Production

To create installable packages for Windows:

```powershell
# From desktop-tauri directory
npm run tauri build
```

This will create:
- **MSI Installer**: `desktop-tauri\src-tauri\target\release\bundle\msi\loopforge-desktop_<version>_x64.msi`
- **NSIS Installer**: `desktop-tauri\src-tauri\target\release\bundle\nsis\loopforge-desktop_<version>_x64-setup.exe`

### Build for Other Platforms

```bash
# macOS
npm run tauri build
# Output: DMG file in target/release/bundle/dmg/

# Linux
npm run tauri build
# Output: AppImage and/or DEB in target/release/bundle/
```

---

## 🎯 Step 5: Run the Installed App

After installation, you can:

1. **From Start Menu**: Find "LoopForge Desktop" in your Start Menu
2. **From Desktop**: Use the desktop shortcut
3. **From Command Line**: Run the executable directly

The app will:
1. Automatically start the Go backend
2. Open the main window
3. All features work exactly like the web version

---

## 📚 Common Commands

| Command | Description |
|---------|-------------|
| `npm install` | Install Node.js dependencies |
| `npm run build:backend` | Build Go backend |
| `npm run tauri dev` | Run in development mode |
| `npm run tauri build` | Build production installers |
| `npm run tauri clean` | Clean build artifacts |
| `npm run tauri -- help` | Show Tauri help |

---

## 🔍 Troubleshooting

### Issue: Backend won't start

**Symptoms**: App opens but says "Backend not running"

**Solutions**:
1. Check if another instance is already running
2. Verify the backend binary exists: `desktop-tauri\src-tauri\resources\loopforge-backend.exe`
3. Check logs: `desktop-tauri\src-tauri\resources\backend.log`
4. Try running manually: `cd desktop-tauri && npm run build:backend && npm run tauri dev`

### Issue: Port already in use

**Symptoms**: "Port 8090 already in use"

**Solutions**:
1. Find and kill the process using port 8090:
   ```powershell
   netstat -ano | findstr :8090
   taskkill /PID <PID> /F
   ```
2. Or use a different port (edit `desktop-tauri\src-tauri\tauri.conf.json`)

### Issue: Docker not running

**Symptoms**: Docker commands fail

**Solutions**:
1. Start Docker Desktop
2. Wait for Docker to fully initialize (check tray icon)
3. Restart the app

### Issue: CORS errors

**Symptoms**: API requests blocked by CORS

**Solutions**:
1. Ensure you're using the same origin (127.0.0.1:8090)
2. Verify the backend has CORS headers (check `internal/httpapi/gin_helpers.go`)
3. Add the header: `c.Header("Access-Control-Allow-Credentials", "true")`

---

## 🧪 Testing

### Run the Test Suite

```powershell
# Quick test (file structure only)
cd desktop-tauri
..\desktop-tauri\test-tauri-app.ps1 -QuickTest

# Full test (includes build checks)
cd desktop-tauri
..\desktop-tauri\test-tauri-app.ps1

# Skip build tests
cd desktop-tauri
..\desktop-tauri\test-tauri-app.ps1 -SkipBuild
```

### Verify Specific Features

After starting the app, test these key features:

1. **Login**: Test login with your credentials
2. **Chat**: Send a message to the bot
3. **SOP Designer**: Create a new SOP
4. **Workspace**: Start a conversation workspace
5. **Runs**: Run an SOP and check the logs
6. **File Upload**: Upload a skill or MCP config
7. **File Download**: Download run logs or artifacts

---

## 📖 Documentation

For more detailed information, see:

| Document | Description |
|----------|-------------|
| `README.md` | User guide and installation |
| `DEVELOPMENT.md` | Development workflow and architecture |
| `SUMMARY.md` | Implementation summary |
| `VERIFICATION_REPORT.md` | Comprehensive verification by subagents |
| `FINAL_SUMMARY.md` | Complete project overview |

---

## 🎯 Tips & Tricks

### Development Workflow

1. **Hot Reload**: Changes to frontend files are picked up automatically in dev mode
2. **Backend Changes**: Rebuild the backend after changes: `npm run build:backend`
3. **Rust Changes**: Tauri will detect Rust changes and rebuild automatically

### Clean Build

```powershell
cd desktop-tauri
rm -rf node_modules
rm -rf src-tauri/target
npm install
npm run build:backend
npm run tauri dev
```

### Custom Configuration

Edit `desktop-tauri\src-tauri\tauri.conf.json` to customize:
- App name and version
- Window size and position
- Permissions
- Bundle configuration

---

## 🤝 Getting Help

If you encounter issues:

1. **Check the logs**: `desktop-tauri\src-tauri\resources\backend.log`
2. **Verify dependencies**: Run `verify-installation.ps1`
3. **Consult documentation**: Read the files in `desktop-tauri/`
4. **Check Tauri docs**: [tauri.app](https://tauri.app/)

---

## 🏆 Success!

You now have a **fully functional native Windows desktop app** for LoopForge! 

All features work exactly as they do in the web version:
- ✅ Chat Workbench
- ✅ Conversation Workspaces  
- ✅ SOP Designer & Runs
- ✅ Authentication & Projects
- ✅ File Upload/Download
- ✅ Docker Integration

Enjoy your desktop experience! 🎉
