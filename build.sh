#!/bin/bash
# Simple build script for gotoni binaries

set -e

VERSION=${1:-"latest"}
BUILD_DIR="dist"

echo "Building gotoni binaries..."

# Create build directory
mkdir -p $BUILD_DIR

# Build for current platform
echo "Building for $(go env GOOS)/$(go env GOARCH)..."
go build -o $BUILD_DIR/gotoni-$(go env GOOS)-$(go env GOARCH)$(go env GOEXE) .

# Build for common platforms
echo "Building for linux/amd64..."
GOOS=linux GOARCH=amd64 go build -o $BUILD_DIR/gotoni-linux-amd64 .

echo "Building for linux/arm64..."
GOOS=linux GOARCH=arm64 go build -o $BUILD_DIR/gotoni-linux-arm64 .

echo "Building for darwin/amd64..."
GOOS=darwin GOARCH=amd64 go build -o $BUILD_DIR/gotoni-darwin-amd64 .

echo "Building for darwin/arm64..."
GOOS=darwin GOARCH=arm64 go build -o $BUILD_DIR/gotoni-darwin-arm64 .

echo "Building for windows/amd64..."
GOOS=windows GOARCH=amd64 go build -o $BUILD_DIR/gotoni-windows-amd64.exe .

echo "Done! Binaries are in the $BUILD_DIR directory:"
ls -lh $BUILD_DIR/

