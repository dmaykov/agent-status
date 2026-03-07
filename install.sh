#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}"
NOCTALIA_PLUGIN_DIR="$CONFIG_DIR/noctalia/plugins/agent-status"

mkdir -p "$NOCTALIA_PLUGIN_DIR"

cp "$ROOT_DIR/plugin/agent-status/manifest.json" "$NOCTALIA_PLUGIN_DIR/manifest.json"
cp "$ROOT_DIR/plugin/agent-status/Main.qml" "$NOCTALIA_PLUGIN_DIR/Main.qml"
cp "$ROOT_DIR/plugin/agent-status/BarWidget.qml" "$NOCTALIA_PLUGIN_DIR/BarWidget.qml"
cp "$ROOT_DIR/plugin/agent-status/Panel.qml" "$NOCTALIA_PLUGIN_DIR/Panel.qml"
cp "$ROOT_DIR/plugin/agent-status/agent_status_helper.py" "$NOCTALIA_PLUGIN_DIR/agent_status_helper.py"
chmod +x "$NOCTALIA_PLUGIN_DIR/agent_status_helper.py"

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
