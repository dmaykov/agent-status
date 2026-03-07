# Agent Status Panel

This folder contains the source for the `agent-status` Noctalia plugin that shows AI agents per Niri workspace.

## What is here

- `plugin/`
  - `manifest.json`
  - `Main.qml`
  - `BarWidget.qml`
  - `Panel.qml`
  - `icons/`
- `helper/`
  - Go helper split across multiple files
- `examples/`
  - `plugins.json.fragment`
  - `settings.left-widgets.json`
  - `niri-ai-keybinds.kdl`
- `install.sh`

## What the plugin does

- Adds a bar widget that summarizes AI agents on the focused workspace.
- Opens a panel listing tracked agents on the focused workspace.
- Detects live `codex` and `claude` terminals from Niri + `/proc`.
- Refreshes from `niri msg -j event-stream` with a short debounce instead of a 1 second poll loop.
- Recovers prompts after the fact:
  - Codex from `~/.codex/state_5.sqlite`
  - Claude from `~/.claude/projects/**/*.jsonl`
- Uses wrapper metadata from `~/.cache/agent-status/sessions/*.session` when present.

## Install

Run:

```bash
./install.sh
```

That copies the plugin into:

```text
~/.config/noctalia/plugins/agent-status
```

## Manual Noctalia config

You still need to wire the plugin into your Noctalia config yourself.

1. Enable it in `~/.config/noctalia/plugins.json`
   Example fragment: `examples/plugins.json.fragment`
2. Add `plugin:agent-status` to your bar widgets in `~/.config/noctalia/settings.json`
   Example left section: `examples/settings.left-widgets.json`
   Remove the separate `ActiveWindow` entry if you want the agent widget to act as the only active-window fallback.
3. Restart Noctalia:

```bash
systemctl --user restart noctalia-shell.service
```

## Launching agents

The plugin works with direct `alacritty + codex/claude` launches.

Example Niri bindings are in `examples/niri-ai-keybinds.kdl`.

## Dependencies and assumptions

- Linux with `/proc`
- `niri` available on `$PATH`
- Noctalia / Quickshell plugin support
- Go 1.26+ to build the helper during install
- `sqlite3`
- Alacritty windows for the tracked terminal agents

## Editing workflow

Edit the source here:

- `plugin/*.qml`
- `helper/*.go`

Then reinstall by rerunning:

```bash
./install.sh
```

and restart Noctalia.
