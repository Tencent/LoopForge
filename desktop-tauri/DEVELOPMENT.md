# LoopForge Desktop - Development Guide

This guide covers the development workflow for the LoopForge Desktop application.

## Quick Start

### Prerequisites

Ensure you have the following installed:

1. **Node.js** 18+ (recommended: 20+)
2. **npm** or **yarn**
3. **Rust** 1.70+ (for Tauri)
4. **Go** 1.26+ (for backend)
5. **Tauri CLI**: `npm install --global @tauri-apps/cli`
6. **Docker** (for sandbox execution)

### Installation

```bash
# Clone the repository (if not already done)
git clone https://github.com/Tencent/LoopForge.git
cd LoopForge

# Install Tauri dependencies
cd desktop-tauri
npm install

# Install Rust dependencies
cargo tauri deps
```

## Running in Development Mode

There are two ways to run in development mode:

### Method 1: External Backend (Recommended)

Run the backend separately using Docker Compose, then start Tauri:

```bash
# Terminal 1: Start backend
cd /path/to/LoopForge
./start.sh

# Terminal 2: Start Tauri dev
cd desktop-tauri
TAURI_ENV_DEV=true npm run tauri dev
```

### Method 2: Embedded Backend

Build the Go backend and run Tauri with the embedded backend:

```bash
# On Windows (PowerShell)
cd desktop-tauri
npm run build:backend
TAURI_ENV_DEV=false npm run tauri dev

# On Unix
cd desktop-tauri
npm run build:backend
TAURI_ENV_DEV=false npm run tauri dev
```

## Building for Production

### Windows

```powershell
# Using PowerShell
cd desktop-tauri
npm run build:backend
npm run tauri build

# The installer will be in:
# desktop-tauri/src-tauri/target/release/bundle/msi/
# desktop-tauri/src-tauri/target/release/bundle/nsis/
```

### macOS

```bash
cd desktop-tauri
npm run build:backend
npm run tauri build

# The app will be in:
# desktop-tauri/src-tauri/target/release/bundle/dmg/
# desktop-tauri/src-tauri/target/release/bundle/macos/
```

### Linux

```bash
cd desktop-tauri
npm run build:backend
npm run tauri build

# The installer will be in:
# desktop-tauri/src-tauri/target/release/bundle/appimage/
# desktop-tauri/src-tauri/target/release/bundle/deb/
```

## Project Structure

```
desktop-tauri/
├── src/                       # Frontend sources
│   ├── index.html            # Main HTML wrapper
│   └── tauri-interface.js    # Tauri API wrapper
│
├── src-tauri/                # Rust backend
│   ├── Cargo.toml            # Rust dependencies
│   ├── tauri.conf.json      # Tauri configuration
│   ├── icons/               # App icons
│   └── src/
│       ├── main.rs          # Main Rust code
│       └── proxy.rs         # Proxy utilities
│
├── scripts/                  # Build scripts
│   ├── build-backend.sh     # Build Go backend (Unix)
│   └── build-backend.ps1    # Build Go backend (Windows)
│
├── package.json             # Node.js configuration
├── vite.config.js           # Vite configuration
├── build.bat                # Windows build script
├── build.ps1                # PowerShell build script
└── README.md                # Documentation
```

## Architecture

### Communication Flow

```
┌─────────────────────────────────────────────────────────────┐
│                      LoopForge Desktop                        │
├─────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────────┐    ┌──────────────────┐               │
│  │   Tauri WebView  │    │   Rust Backend    │               │
│  │   (index.html)   │    │    (main.rs)      │               │
│  └────────┬─────────┘    └────────┬─────────┘               │
│           │                        │                         │
│           │ TAURI COMMANDS         │                         │
│           ▼                        ▼                         │
│  ┌──────────────────┐    ┌──────────────────┐               │
│  │   Go Backend     │◄───┤   Process        │               │
│  │   (loopforge)    │    │   Management     │               │
│  └──────────────────┘    └──────────────────┘               │
│          ▲                                                  │
│          │ HTTP Requests                                    │
│          ▼                                                  │
│  ┌──────────────────┐                                        │
│  │   Docker         │                                        │
│  │   Sandboxes      │                                        │
│  └──────────────────┘                                        │
│                                                                 │
└─────────────────────────────────────────────────────────────┘
```

### Key Components

1. **Tauri WebView**: Serves the web frontend and handles rendering
2. **Rust Backend**: Manages the Go backend process and provides Tauri commands
3. **Go Backend**: The existing LoopForge server that handles all business logic
4. **Docker**: Used for sandbox execution (required for SOP runs)

## Tauri Commands

The following commands are available to the frontend:

### Backend Management
- `start_backend` - Start the Go backend server
- `stop_backend` - Stop the Go backend server
- `restart_backend` - Restart the Go backend server
- `get_backend_status` - Check if backend is running
- `get_backend_url_cmd` - Get the backend URL
- `get_web_app_url_cmd` - Get the web app URL

### System Information
- `get_app_version` - Get the app version
- `get_platform_info` - Get OS and architecture
- `get_data_directory` - Get the data directory path
- `set_data_directory` - Set the data directory path
- `check_docker_running` - Check if Docker is running

### Utilities
- `open_url` - Open an external URL

## Frontend Integration

The existing web frontend needs minimal changes to work with Tauri. The main differences are:

1. **API Requests**: All API requests should go to the backend URL returned by `get_backend_url_cmd()`
2. **File Access**: Use Tauri's filesystem API for file operations
3. **External URLs**: Use the `open_url` command to open external URLs

### Example: Fetching Data

```javascript
import { backend } from './tauri-interface';

async function fetchData(endpoint) {
    const backendUrl = await backend.getUrl();
    const response = await fetch(`${backendUrl}${endpoint}`);
    return await response.json();
}
```

### Example: Opening External URL

```javascript
import { utils } from './tauri-interface';

async function openExternalUrl(url) {
    await utils.openUrl(url);
}
```

## Debugging

### Rust Backend

```bash
# Run with verbose logging
RUST_LOG=info npm run tauri dev

# Or set in code
std::env::set_var("RUST_LOG", "debug");
```

### Go Backend

The Go backend output is captured and logged by the Rust backend. You can see it in the Tauri console.

### Web Frontend

Use Chrome DevTools to debug the web frontend:

1. Open the app
2. Press `F12` or `Ctrl+Shift+I` to open DevTools
3. Use the Console, Network, and Sources tabs

## Troubleshooting

### Backend Not Starting

1. **Check Go installation**: Run `go version`
2. **Check backend binary**: Ensure `loopforge-backend` exists in the project
3. **Check permissions**: Ensure the binary has execute permissions
4. **Check logs**: Look at the Tauri console for error messages

### Frontend Not Loading

1. **Check Vite**: Run `npm run build:frontend`
2. **Check paths**: Ensure all resources are in the correct locations
3. **Check console**: Look for JavaScript errors in the browser console

### Docker Not Available

1. **Check Docker installation**: Run `docker --version`
2. **Check Docker Desktop**: Ensure it's running on Windows/macOS
3. **Check permissions**: Ensure the app has access to Docker

### Permission Issues

On Linux/macOS, you might need to grant additional permissions:

```bash
# Allow Tauri to access required resources
sudo xattr -d com.apple.quarantine /Applications/LoopForge.app
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `TAURI_ENV_DEV` | Run in development mode | `false` |
| `LOOPFORGE_HOST` | Backend host | `127.0.0.1` |
| `LOOPFORGE_PORT` | Backend port | `8090` |
| `LOOPFORGE_DATA_DIR` | Data directory | `loopforge-data` |
| `LOOPFORGE_ENV` | Environment mode | `production` |

### tauri.conf.json

The main configuration file contains settings for:
- App metadata (name, version, description)
- Window configuration (size, minimum size, resizable, etc.)
- Permissions (filesystem, HTTP, process access)
- Bundle settings (icons, installers, platform-specific options)

## Testing

### Unit Tests

```bash
# Test Rust code
cd src-tauri
cargo test

# Test frontend code
npm test
```

### Integration Tests

1. Start the backend in dev mode
2. Run Tauri in dev mode
3. Manually test all features

### End-to-End Tests

1. Build the production app
2. Install and run it
3. Test all features

## Deployment

### Windows

1. Build the installer:
   ```powershell
   npm run tauri build
   ```

2. The installer will be in:
   - `desktop-tauri/src-tauri/target/release/bundle/msi/` (MSI installer)
   - `desktop-tauri/src-tauri/target/release/bundle/nsis/` (NSIS installer)

3. Distribute the installer to users

### macOS

1. Build the app:
   ```bash
   npm run tauri build
   ```

2. The app will be in:
   - `desktop-tauri/src-tauri/target/release/bundle/dmg/` (DMG installer)
   - `desktop-tauri/src-tauri/target/release/bundle/macos/` (.app bundle)

3. Notarize the app (if distributing outside the Mac App Store)
4. Distribute the app to users

### Linux

1. Build the app:
   ```bash
   npm run tauri build
   ```

2. The installer will be in:
   - `desktop-tauri/src-tauri/target/release/bundle/appimage/` (.AppImage)
   - `desktop-tauri/src-tauri/target/release/bundle/deb/` (.deb package)

3. Distribute the installer to users

## Contributing

To contribute to the desktop app:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly on all target platforms
5. Submit a pull request

## License

MIT License - see LICENSE file in the project root.
