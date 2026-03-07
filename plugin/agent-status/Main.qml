import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons

Item {
  id: root

  required property var pluginApi

  visible: false
  width: 0
  height: 0

  readonly property string cacheBaseDir: (Quickshell.env("XDG_CACHE_HOME") || (Quickshell.env("HOME") + "/.cache")) + "/agent-status"
  readonly property string sessionsDir: cacheBaseDir + "/sessions"
  readonly property string stateFilePath: cacheBaseDir + "/state.json"
  property var stateData: defaultState()
  readonly property var focusedWorkspace: stateData.focused_workspace || null

  function defaultState() {
    return {
      "focused_workspace": {
        "id": -1,
        "idx": -1,
        "name": "",
        "active_window_title": "No active window",
        "summary_text": "No active window",
        "primary_prompt_line": "",
        "agent_count": 0,
        "agents": []
      },
      "workspaces": []
    };
  }

  function loadStateText(text) {
    if (!text || String(text).trim() === "") {
      stateData = defaultState();
      return;
    }

    try {
      var parsed = JSON.parse(String(text));
      stateData = parsed && typeof parsed === "object" ? parsed : defaultState();
    } catch (error) {
      Logger.w("AgentStatus", "Failed to parse state file:", error);
      stateData = defaultState();
    }
  }

  Process {
    id: helperProcess

    command: ["python3", root.pluginApi.pluginDir + "/agent_status_helper.py"]
    workingDirectory: root.pluginApi.pluginDir
    running: true

    stderr: SplitParser {
      splitMarker: "\n"
      onRead: function (data) {
        var line = String(data || "").trim();
        if (line.length > 0) {
          Logger.w("AgentStatusHelper", line);
        }
      }
    }

    onExited: function () {
      helperRestart.start();
    }
  }

  Timer {
    id: helperRestart
    interval: 2000
    repeat: false
    onTriggered: {
      helperProcess.running = true;
    }
  }

  FileView {
    id: stateFile

    path: root.stateFilePath
    preload: true
    watchChanges: true
    printErrors: false

    onLoaded: {
      root.loadStateText(stateFile.text());
    }

    onFileChanged: {
      stateFile.reload();
    }

    onLoadFailed: function () {
      root.stateData = root.defaultState();
    }
  }

  Timer {
    id: stateRefresh
    interval: 1000
    repeat: true
    running: true
    onTriggered: stateFile.reload()
  }

  Component.onCompleted: {
    Quickshell.execDetached(["mkdir", "-p", root.cacheBaseDir]);
    Quickshell.execDetached(["mkdir", "-p", root.sessionsDir]);
    stateFile.reload();
  }

  Component.onDestruction: {
    helperProcess.running = false;
  }
}
