#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}"
NOCTALIA_PLUGIN_DIR="$CONFIG_DIR/noctalia/plugins/agent-status"
HELPER_SRC_DIR="$ROOT_DIR/plugin/agent-status/helper"
HELPER_BINARY="$NOCTALIA_PLUGIN_DIR/agent_status_helper"

mkdir -p "$NOCTALIA_PLUGIN_DIR"
mkdir -p "$NOCTALIA_PLUGIN_DIR/icons"

cp "$ROOT_DIR/plugin/agent-status/manifest.json" "$NOCTALIA_PLUGIN_DIR/manifest.json"
cp "$ROOT_DIR/plugin/agent-status/Main.qml" "$NOCTALIA_PLUGIN_DIR/Main.qml"
cp "$ROOT_DIR/plugin/agent-status/BarWidget.qml" "$NOCTALIA_PLUGIN_DIR/BarWidget.qml"
cp "$ROOT_DIR/plugin/agent-status/Panel.qml" "$NOCTALIA_PLUGIN_DIR/Panel.qml"
cp "$ROOT_DIR/plugin/agent-status/icons/chatgpt-icon.png" "$NOCTALIA_PLUGIN_DIR/icons/chatgpt-icon.png"
cp "$ROOT_DIR/plugin/agent-status/icons/claude-ai-icon.png" "$NOCTALIA_PLUGIN_DIR/icons/claude-ai-icon.png"
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
