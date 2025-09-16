#!/bin/bash

# Cross-platform build script for ZX Spectrum +3 disk image tools
# Builds binaries for multiple operating systems and architectures

set -e

BUILD_DIR="build"
VERSION="${VERSION:-$(date +%Y%m%d)}"

# Clean and create build directory
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo "Building ZX Spectrum +3 tools v$VERSION..."

# Define target platforms (GOOS/GOARCH combinations)
declare -a TARGETS=(
    "darwin/amd64"      # macOS Intel
    "darwin/arm64"      # macOS Apple Silicon
    "windows/amd64"     # Windows 64-bit
    "windows/386"       # Windows 32-bit
    "linux/amd64"       # Linux 64-bit
    "linux/386"         # Linux 32-bit
    "linux/arm"         # Linux ARM (Raspberry Pi)
    "linux/arm64"       # Linux ARM64
    "freebsd/amd64"     # FreeBSD 64-bit
    "freebsd/386"       # FreeBSD 32-bit
    "netbsd/amd64"      # NetBSD 64-bit
    "netbsd/386"        # NetBSD 32-bit
    "openbsd/amd64"     # OpenBSD 64-bit
    "openbsd/386"       # OpenBSD 32-bit
)

# Source files to build
declare -a SOURCES=(
    "zx3dsk.go"
    "zx3info.go"
    "zx3extract.go"
)

# Build for each target platform
for target in "${TARGETS[@]}"; do
    IFS='/' read -r GOOS GOARCH <<< "$target"
    
    echo "Building for $GOOS/$GOARCH..."
    
    # Create platform-specific directory
    PLATFORM_DIR="$BUILD_DIR/$GOOS-$GOARCH"
    mkdir -p "$PLATFORM_DIR"
    
    # Set environment variables
    export GOOS GOARCH
    
    # Build each tool
    for source in "${SOURCES[@]}"; do
        if [[ ! -f "$source" ]]; then
            echo "Warning: Source file $source not found, skipping..."
            continue
        fi
        
        # Extract binary name from source file
        BINARY_NAME="${source%.go}"
        
        # Add .exe extension for Windows
        if [[ "$GOOS" == "windows" ]]; then
            BINARY_NAME="${BINARY_NAME}.exe"
        fi
        
        # Build the binary
        go build -ldflags "-s -w" -o "$PLATFORM_DIR/$BINARY_NAME" "$source"
        
        if [[ $? -eq 0 ]]; then
            echo "  Built $BINARY_NAME"
        else
            echo "  Failed to build $BINARY_NAME"
        fi
    done
    
    # Copy documentation to each platform directory
    if [[ -f "README.md" ]]; then
        cp README.md "$PLATFORM_DIR/"
    fi
    if [[ -f "LICENSE" ]]; then
        cp LICENSE "$PLATFORM_DIR/"
    fi
    
    echo "  Output: $PLATFORM_DIR/"
done

# Create archives for distribution
echo ""
echo "Creating distribution archives..."

cd "$BUILD_DIR"

for target in "${TARGETS[@]}"; do
    IFS='/' read -r GOOS GOARCH <<< "$target"
    PLATFORM_DIR="$GOOS-$GOARCH"
    
    if [[ -d "$PLATFORM_DIR" ]]; then
        if [[ "$GOOS" == "windows" ]]; then
            # Create ZIP for Windows
            ARCHIVE_NAME="zx3tools-$VERSION-$GOOS-$GOARCH.zip"
            zip -r "$ARCHIVE_NAME" "$PLATFORM_DIR" > /dev/null
            echo "  Created $ARCHIVE_NAME"
        else
            # Create tar.gz for Unix-like systems
            ARCHIVE_NAME="zx3tools-$VERSION-$GOOS-$GOARCH.tar.gz"
            tar -czf "$ARCHIVE_NAME" "$PLATFORM_DIR"
            echo "  Created $ARCHIVE_NAME"
        fi
    fi
done

cd ..

echo ""
echo "Build complete. Binaries and archives are in the $BUILD_DIR directory."
echo ""
echo "Available platforms:"
for target in "${TARGETS[@]}"; do
    IFS='/' read -r GOOS GOARCH <<< "$target"
    echo "  $GOOS-$GOARCH"
done