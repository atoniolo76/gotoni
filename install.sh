#!/bin/bash
# gotoni installation script

set -e

REPO="atoniolo76/gotoni"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="gotoni"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

# Determine binary name
if [ "$OS" = "windows" ] || [ "$OS" = "mingw" ]; then
  BINARY_FILE="gotoni-${OS}-${ARCH}.exe"
else
  BINARY_FILE="gotoni-${OS}-${ARCH}"
fi

# Get latest release URL
LATEST_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_FILE}"

echo "Installing gotoni..."
echo "Platform: ${OS}-${ARCH}"
echo "Downloading from: ${LATEST_URL}"

# Download binary
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

if ! curl -fsSL "$LATEST_URL" -o "$TMP_DIR/$BINARY_NAME"; then
  echo "Error: Failed to download binary. Make sure the release exists and your platform is supported."
  exit 1
fi

# Make executable
chmod +x "$TMP_DIR/$BINARY_NAME"

# Install to /usr/local/bin (requires sudo)
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
else
  echo "Installing to $INSTALL_DIR (requires sudo)..."
  sudo mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
fi

echo "✓ gotoni installed successfully to $INSTALL_DIR/$BINARY_NAME"
echo ""
echo "Run 'gotoni --help' to get started!"

