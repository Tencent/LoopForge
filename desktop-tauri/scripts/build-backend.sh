#!/bin/bash

# Build script for the Go backend
# This script builds the Go backend for the current platform

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print colored messages
info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../" && pwd)"
BUILD_DIR="$PROJECT_ROOT/desktop-tauri/bin"

info "Building Go backend..."
info "Project root: $PROJECT_ROOT"
info "Build directory: $BUILD_DIR"

# Create build directory
mkdir -p "$BUILD_DIR"

# Change to project root
cd "$PROJECT_ROOT"

# Check if Go is installed
if ! command -v go &> /dev/null; then
    error "Go is not installed. Please install Go 1.26+ first."
fi

info "Go version: $(go version)"

# Get the platform info
OS="$(uname -s)"
ARCH="$(uname -m)"

info "Platform: $OS $ARCH"

# Determine output name
if [ "$OS" = "Darwin" ]; then
    OUTPUT="loopforge-backend-macos"
    CGO_ENABLED=1
    CC="clang"
elif [ "$OS" = "Linux" ]; then
    OUTPUT="loopforge-backend-linux"
    CGO_ENABLED=1
    CC="gcc"
elif [ "$OS" = "Windows" ] || [ "$OS" = "MINGW*" ]; then
    OUTPUT="loopforge-backend.exe"
    CGO_ENABLED=1
    # On Windows, we'll handle it differently
    if [ "$ARCH" = "x86_64" ]; then
        GOOS="windows"
        GOARCH="amd64"
    else
        GOOS="windows"
        GOARCH="$ARCH"
    fi
else
    error "Unsupported platform: $OS"
fi

# Build the backend
info "Building Go backend for $GOOS $GOARCH..."

# Set build flags
LDFLAGS="-s -w"
BUILD_FLAGS="-trimpath -ldflags $LDFLAGS"

if [ -n "$GOOS" ]; then
    BUILD_FLAGS="$BUILD_FLAGS -GOOS=$GOOS"
fi

if [ -n "$GOARCH" ]; then
    BUILD_FLAGS="$BUILD_FLAGS -GOARCH=$GOARCH"
fi

# The main backend is in cmd/agentflow
if [ -d "$PROJECT_ROOT/cmd/agentflow" ]; then
    info "Building agentflow server..."
    
    # Build for the appropriate platform
    if [ "$OS" = "Windows" ] || [ -n "$GOOS" ]; then
        GOOS=${GOOS:-windows}
        GOARCH=${GOARCH:-amd64}
        
        info "Cross-compiling for $GOOS/$GOARCH..."
        
        cd "$PROJECT_ROOT/cmd/agentflow"
        CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build \
            -o "$BUILD_DIR/loopforge-backend" \
            $BUILD_FLAGS \
            .
    else
        # Native build
        cd "$PROJECT_ROOT/cmd/agentflow"
        CGO_ENABLED=1 go build \
            -o "$BUILD_DIR/loopforge-backend" \
            $BUILD_FLAGS \
            .
    fi
    
    info "Build completed successfully!"
    info "Output: $BUILD_DIR/loopforge-backend"
    
    # Copy to desktop-tauri directory for easier access
    cp "$BUILD_DIR/loopforge-backend" "$PROJECT_ROOT/desktop-tauri/src-tauri/"
    
    # On Windows, also copy with .exe extension if needed
    if [ "$GOOS" = "windows" ] && [ ! -f "$PROJECT_ROOT/desktop-tauri/src-tauri/loopforge-backend.exe" ]; then
        cp "$BUILD_DIR/loopforge-backend" "$PROJECT_ROOT/desktop-tauri/src-tauri/loopforge-backend.exe"
    fi
    
else
    error "Could not find cmd/agentflow directory"
fi

# Clean up
info "Cleanup..."

# Success
echo ""
info "Go backend built successfully!"
info "Binary location: $BUILD_DIR/loopforge-backend"
