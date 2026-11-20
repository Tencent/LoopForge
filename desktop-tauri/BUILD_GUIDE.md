# LoopForge Desktop - Build Guide

## ⚠️ Important: Go Version Requirement

**LoopForge requires Go 1.25 or earlier** due to breaking changes in Go 1.26 with the `syscall` package.

If you have Go 1.26+ installed, you have two options:

### Option 1: Use Go 1.25 (Recommended)

1. **Install Go 1.25**: Download from https://go.dev/dl/go1.25.11.windows-amd64.msi
2. **Set GOROOT**: Make sure Go 1.25 is in your PATH before Go 1.26
3. **Verify**: `go version` should show `go1.25.x`

### Option 2: Use Docker (Cross-Platform)

Build the backend in a Docker container with Go 1.25:

```powershell
# From desktop-tauri directory
docker run --rm -v ${PWD}/..:/workspace -w /workspace golang:1.25-alpine \
    sh -c "cd cmd/agentflow && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o /workspace/desktop-tauri/src-tauri/loopforge-backend.exe ."
```

### Option 3: Downgrade Go

```powershell
# Uninstall Go 1.26
choco uninstall golang

# Install Go 1.25
choco install golang --version=1.25.11
```

---

## 🚀 Standard Build (With Go 1.25)

### Step 1: Install Dependencies

Ensure you have:
- ✅ Node.js 16+ (for Tauri CLI)
- ✅ Rust 1.70+ (for Tauri backend)
- ✅ Go 1.25.x (for LoopForge backend)
- ✅ Docker (for runtime)

Verify:
```powershell
node --version  # v16.0.0 or higher
npm --version   # 8.0.0 or higher
rustc --version # 1.70.0 or higher
cargo --version # 1.70.0 or higher
go version      # go1.25.x (NOT 1.26+)
docker --version
```

### Step 2: Install Tauri Dependencies

From `desktop-tauri/` directory:

```powershell
cd desktop-tauri
npm install
```

This installs:
- Tauri CLI
- Vite
- Other required packages

### Step 3: Build the Go Backend

```powershell
cd cmd/agentflow
go build -trimpath -ldflags "-s -w" -o ..\..\desktop-tauri\src-tauri\loopforge-backend.exe .
```

**Expected output**: `loopforge-backend.exe` in `desktop-tauri\src-tauri\`

### Step 4: Build the Tauri App

From `desktop-tauri/` directory:

```powershell
npm run tauri build
```

This will:
1. Build the Tauri application
2. Package it into installers
3. Output to `src-tauri/target/release/bundle/`

### Step 5: Install and Run

**Windows**:
- MSI: `src-tauri/target/release/bundle/msi/loopforge-desktop_<version>_x64.msi`
- NSIS: `src-tauri/target/release/bundle/nsis/loopforge-desktop_<version>_x64-setup.exe`

**macOS**:
- DMG: `src-tauri/target/release/bundle/dmg/loopforge-desktop_<version>_x64.dmg`

**Linux**:
- AppImage: `src-tauri/target/release/bundle/appimage/loopforge-desktop_<version>_amd64.AppImage`
- DEB: `src-tauri/target/release/bundle/deb/loopforge-desktop_<version>_amd64.deb`

---

## 🐳 Docker Build (Recommended for Go 1.26+ Users)

If you have Go 1.26+ installed, use Docker to build the backend:

### Build Backend in Docker

```powershell
# From LoopForge project root
cd desktop-tauri
docker run --rm \
    -v ${PWD}/..:/workspace \
    -w /workspace \
    golang:1.25-alpine \
    sh -c "cd cmd/agentflow && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o /workspace/desktop-tauri/src-tauri/loopforge-backend.exe ."
```

### Then Build Tauri

```powershell
npm run tauri build
```

---

## 📦 Alternative: Use Pre-Built Backend

If you can't build the Go backend, you can:

1. **Download a pre-built binary** from LoopForge releases
2. **Copy it** to `desktop-tauri\src-tauri\loopforge-backend.exe`
3. **Build Tauri** with `npm run tauri build`

---

## 🔧 Development Build

For development (hot-reload, faster builds):

```powershell
cd desktop-tauri
npm run tauri dev
```

This will:
- Start Tauri in development mode
- Auto-start the Go backend (if built)
- Enable hot-reload for frontend changes
- Show logs in console

---

## ⚡ Quick Build Script

Create a PowerShell script `build-all.ps1`:

```powershell
# build-all.ps1
cd desktop-tauri

# Install dependencies
npm install

# Build Go backend (requires Go 1.25)
cd ..\cmd\agentflow
go build -trimpath -ldflags "-s -w" -o ..\..\desktop-tauri\src-tauri\loopforge-backend.exe .

# Build Tauri app
cd ..\desktop-tauri
npm run tauri build

Write-Host "Build completed! Installers are in: $PWD\src-tauri\target\release\bundle\" -ForegroundColor Green
```

Run it:
```powershell
.\desktop-tauri\build-all.ps1
```

---

## 🛠️ Troubleshooting

### Error: "undefined: syscall.Stat_t" or "Setpgid"

**Cause**: You have Go 1.26+ installed. LoopForge uses deprecated syscall functions.

**Solution**: Use Go 1.25 or build in Docker:
```powershell
# Check Go version
go version

# If it's 1.26+, use Docker
docker run --rm -v ${PWD}:/workspace -w /workspace golang:1.25-alpine \
    go build -trimpath -ldflags '-s -w' -o /workspace/desktop-tauri/src-tauri/loopforge-backend.exe ./cmd/agentflow
```

### Error: "Build failed - output binary not created"

**Solution**:
1. Check Go version
2. Ensure you're in the correct directory
3. Check for compilation errors
4. Try with CGO_ENABLED=0:
```powershell
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o loopforge-backend.exe .
```

### Error: "Docker not running"

**Solution**:
1. Start Docker Desktop
2. Wait for it to fully initialize
3. Check the tray icon
4. Run `docker --version` to verify

### Error: "Tauri CLI not found"

**Solution**:
```powershell
npm install --global @tauri-apps/cli
# or
npm install
```

---

## 📚 Build Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `GOOS` | Target OS | `windows` |
| `GOARCH` | Target architecture | `amd64` |
| `CGO_ENABLED` | Enable CGO | `0` (for cross-compilation) |
| `TAURI_ENV_DEV` | Development mode | `false` |

### Custom Build

Build for different platforms:

```powershell
# Linux
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w" -o loopforge-backend .

# macOS
$env:GOOS = "darwin"
$env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w" -o loopforge-backend .
```

---

## 🎯 Next Steps

After successful build:

1. **Test the installer** on your target platform
2. **Verify all features** work correctly
3. **Create a GitHub release** with the installer
4. **Distribute to users**

---

**Need help?** Check the other documentation files:
- [README.md](README.md) - User guide
- [QUICK_START.md](QUICK_START.md) - Quick start
- [DEVELOPMENT.md](DEVELOPMENT.md) - Development details
- [VERIFICATION_REPORT.md](VERIFICATION_REPORT.md) - Feature verification
