#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}"
NOCTALIA_PLUGIN_DIR="$CONFIG_DIR/noctalia/plugins/agent-status"
PLUGIN_SRC_DIR="$ROOT_DIR/plugin"
HELPER_SRC_DIR="$ROOT_DIR/helper"
HELPER_BINARY="$NOCTALIA_PLUGIN_DIR/agent_status_helper"

confirm() {
  local prompt="$1"
  local reply

  read -r -p "$prompt [y/N] " reply
  [[ "$reply" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]
}

run_as_root() {
  if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
    "$@"
    return
  fi

  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
    return
  fi

  if command -v doas >/dev/null 2>&1; then
    doas "$@"
    return
  fi

  echo "Need root privileges to install Go, but neither sudo nor doas is available." >&2
  exit 1
}

install_go() {
  if command -v pacman >/dev/null 2>&1; then
    run_as_root pacman -Sy --needed go
    return
  fi

  if command -v apt-get >/dev/null 2>&1; then
    run_as_root apt-get update
    run_as_root apt-get install -y golang-go
    return
  fi

  if command -v dnf >/dev/null 2>&1; then
    run_as_root dnf install -y golang
    return
  fi

  if command -v zypper >/dev/null 2>&1; then
    run_as_root zypper install -y go
    return
  fi

  if command -v apk >/dev/null 2>&1; then
    run_as_root apk add go
    return
  fi

  echo "Unsupported package manager. Install Go manually, then rerun ./install.sh." >&2
  exit 1
}

ensure_go() {
  if command -v go >/dev/null 2>&1; then
    return
  fi

  echo "Go was not found on PATH."
  if ! confirm "Install Go now using the system package manager?"; then
    echo "Go is required to build the helper. Install it, then rerun ./install.sh." >&2
    exit 1
  fi

  install_go

  if ! command -v go >/dev/null 2>&1; then
    echo "Go is still unavailable after installation. Open a new shell and rerun ./install.sh." >&2
    exit 1
  fi
}

ensure_go

mkdir -p "$NOCTALIA_PLUGIN_DIR"
mkdir -p "$NOCTALIA_PLUGIN_DIR/icons"

cp "$PLUGIN_SRC_DIR/manifest.json" "$NOCTALIA_PLUGIN_DIR/manifest.json"
cp "$PLUGIN_SRC_DIR/Main.qml" "$NOCTALIA_PLUGIN_DIR/Main.qml"
cp "$PLUGIN_SRC_DIR/BarWidget.qml" "$NOCTALIA_PLUGIN_DIR/BarWidget.qml"
cp "$PLUGIN_SRC_DIR/Panel.qml" "$NOCTALIA_PLUGIN_DIR/Panel.qml"
cp "$PLUGIN_SRC_DIR/icons/chatgpt-icon.png" "$NOCTALIA_PLUGIN_DIR/icons/chatgpt-icon.png"
cp "$PLUGIN_SRC_DIR/icons/claude-ai-icon.png" "$NOCTALIA_PLUGIN_DIR/icons/claude-ai-icon.png"
(
  cd "$HELPER_SRC_DIR"
  go build -o "$HELPER_BINARY" .
)
chmod +x "$HELPER_BINARY"

cat <<EOF
Installed plugin source into:
  $NOCTALIA_PLUGIN_DIR

Next steps:
  1. Enable the plugin in ~/.config/noctalia/plugins.json
  2. Add plugin:agent-status to ~/.config/noctalia/settings.json
  3. Restart Noctalia:
       systemctl --user restart noctalia-shell.service

Config snippets are in:
  $ROOT_DIR/examples
EOF
