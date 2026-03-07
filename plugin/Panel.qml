import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import Quickshell.Widgets
import qs.Commons

Item {
  id: root

  property var pluginApi
  property string selectedProviderOverride: ""
  property bool settingsOpen: false

  property bool allowAttach: true
  property real contentPreferredWidth: Math.round(460 * Style.uiScaleRatio)
  readonly property real baseContentPreferredHeight: Math.max(250, 124 + (agents.length * 92 * Style.uiScaleRatio))
  readonly property real settingsPreferredHeight: settingsOpen ? (settingsContent.implicitHeight + (Style.marginL * 4)) : 0
  property real contentPreferredHeight: Math.round(Math.min(maxPanelHeight, Math.max(baseContentPreferredHeight, settingsPreferredHeight)))

  readonly property var mainState: pluginApi?.mainInstance
  readonly property var focusedWorkspace: mainState ? mainState.focusedWorkspace : null
  readonly property var agents: focusedWorkspace ? (focusedWorkspace.agents || []) : []
  readonly property real maxPanelHeight: ((pluginApi?.panelOpenScreen?.height || 900) * 0.72)
  readonly property var summaryProviderState: mainState?.stateData?.summary_provider || {
    "selected": "auto",
    "effective": "",
    "codex_available": false,
    "claude_available": false
  }
  readonly property string selectedSummaryProvider: selectedProviderOverride !== "" ? selectedProviderOverride : String(summaryProviderState.selected || "auto")
  readonly property string effectiveSummaryProvider: String(summaryProviderState.effective || "")
  readonly property string displayedProviderName: {
    var provider = effectiveSummaryProvider || selectedSummaryProvider;
    return providerDisplayName(provider);
  }
  readonly property string providerModeCaption: {
    if (selectedSummaryProvider === "auto") {
      if (effectiveSummaryProvider !== "") {
        return "Auto fallback";
      }
      return "Auto";
    }
    return "Manual selection";
  }
  readonly property var providerOptions: [
    {
      "value": "auto",
      "label": "Auto",
      "description": "Prefer Codex first, then fall back to Claude if Codex is missing.",
      "detail": effectiveSummaryProvider !== "" ? `Currently using ${providerDisplayName(effectiveSummaryProvider)}.` : "No supported CLI is available right now.",
      "available": true
    },
    {
      "value": "codex",
      "label": "Codex",
      "description": "Generate summary labels with Codex only.",
      "detail": summaryProviderState.codex_available ? "Installed and ready." : "Codex is not installed on this machine.",
      "available": !!summaryProviderState.codex_available
    },
    {
      "value": "claude",
      "label": "Claude",
      "description": "Generate summary labels with Claude only.",
      "detail": summaryProviderState.claude_available ? "Installed and ready." : "Claude is not installed on this machine.",
      "available": !!summaryProviderState.claude_available
    }
  ]

  function iconSourceForTool(tool) {
    if (!pluginApi) {
      return "";
    }
    if (tool === "codex") {
      return pluginApi.pluginDir + "/icons/chatgpt-icon.png";
    }
    if (tool === "claude") {
      return pluginApi.pluginDir + "/icons/claude-ai-icon.png";
    }
    return "";
  }

  function providerDisplayName(provider) {
    if (provider === "codex") {
      return "Codex";
    }
    if (provider === "claude") {
      return "Claude";
    }
    if (provider === "auto") {
      return "Auto";
    }
    return "Unavailable";
  }

  function providerIconSource(provider) {
    if (!pluginApi && (provider === "codex" || provider === "claude")) {
      return "";
    }
    if (provider === "codex") {
      return pluginApi.pluginDir + "/icons/chatgpt-icon.png";
    }
    if (provider === "claude") {
      return pluginApi.pluginDir + "/icons/claude-ai-icon.png";
    }
    return "";
  }

  function providerUsesLocalImage(provider) {
    return provider === "codex" || provider === "claude";
  }

  function providerSelectionStatus(option) {
    if (!option) {
      return "";
    }
    if (option.value === selectedSummaryProvider) {
      if (option.value === "auto" && effectiveSummaryProvider !== "") {
        return `Selected · Using ${providerDisplayName(effectiveSummaryProvider)}`;
      }
      return "Selected";
    }
    if (option.value === "auto") {
      return "Recommended";
    }
    if (!option.available) {
      return "Unavailable";
    }
    return "Available";
  }

  function providerStatusTone(option) {
    if (!option) {
      return Qt.rgba(1, 1, 1, 0.14);
    }
    if (option.value === selectedSummaryProvider) {
      return Qt.rgba(0.31, 0.67, 0.55, 0.22);
    }
    if (!option.available) {
      return Qt.rgba(0.78, 0.37, 0.37, 0.18);
    }
    return Qt.rgba(1, 1, 1, 0.12);
  }

  function providerChoiceEnabled(option) {
    return !!option && (option.value === "auto" || !!option.available) && !providerUpdateProcess.running;
  }

  function setSummaryProvider(provider) {
    if (!pluginApi)
      return;

    selectedProviderOverride = provider;
    providerUpdateProcess.command = [pluginApi.pluginDir + "/agent_status_helper", "set-summary-provider", provider];
    providerUpdateProcess.running = true;
  }

  Process {
    id: providerUpdateProcess

    running: false
    command: []

    stderr: SplitParser {
      splitMarker: "\n"
      onRead: function (data) {
        var line = String(data || "").trim();
        if (line.length > 0) {
          Logger.w("AgentStatusProvider", line);
        }
      }
    }

    onExited: function (exitCode) {
      if (exitCode === 0) {
        root.settingsOpen = false;
        if (root.mainState && root.mainState.restartHelper) {
          root.mainState.restartHelper();
        }
        return;
      }

      root.selectedProviderOverride = "";
    }
  }

  ColumnLayout {
    x: Style.marginL
    y: Style.marginL
    width: parent.width - Style.margin2L
    height: parent.height - Style.margin2L
    spacing: Style.marginM

    RowLayout {
      Layout.fillWidth: true
      spacing: Style.marginM

      ColumnLayout {
        Layout.fillWidth: true
        spacing: Style.margin2XS

        Text {
          Layout.fillWidth: true
          text: focusedWorkspace ? `Workspace ${focusedWorkspace.name || focusedWorkspace.idx}` : "Workspace agents"
          color: Color.mOnSurface
          font.pixelSize: Math.round(18 * Style.uiScaleRatio)
          font.bold: true
          elide: Text.ElideRight
        }

        Text {
          Layout.fillWidth: true
          text: agents.length > 0 ? `${agents.length} tracked agent${agents.length === 1 ? "" : "s"}` : "No tracked agents on this workspace"
          color: Color.mOnSurfaceVariant
          font.pixelSize: Math.round(13 * Style.uiScaleRatio)
          elide: Text.ElideRight
        }
      }

      Rectangle {
        id: settingsChip

        Layout.alignment: Qt.AlignTop
        implicitWidth: chipContent.implicitWidth + (Style.marginM * 2)
        implicitHeight: Math.round(42 * Style.uiScaleRatio)
        radius: height / 2
        color: chipMouse.containsMouse ? Qt.lighter(Style.capsuleColor, 1.1) : Qt.lighter(Style.capsuleColor, 1.03)
        border.color: root.settingsOpen ? Color.mSecondary : Style.capsuleBorderColor
        border.width: Style.capsuleBorderWidth

        Behavior on color {
          ColorAnimation {
            duration: Style.animationNormal
          }
        }

        RowLayout {
          id: chipContent

          anchors.centerIn: parent
          spacing: Style.marginS

          Rectangle {
            Layout.preferredWidth: Math.round(26 * Style.uiScaleRatio)
            Layout.preferredHeight: Math.round(26 * Style.uiScaleRatio)
            radius: width / 2
            color: root.settingsOpen ? Qt.rgba(0.31, 0.67, 0.55, 0.22) : Qt.rgba(1, 1, 1, 0.08)
            border.color: Qt.rgba(1, 1, 1, 0.06)
            border.width: 1

            Text {
              anchors.centerIn: parent
              text: "⚙"
              color: Color.mOnSurface
              font.pixelSize: Math.round(14 * Style.uiScaleRatio)
            }
          }

          ColumnLayout {
            spacing: 0

            Text {
              text: root.displayedProviderName.toLowerCase()
              color: Color.mOnSurface
              font.pixelSize: Math.round(11 * Style.uiScaleRatio)
              font.weight: Font.DemiBold
            }

            Text {
              text: root.providerModeCaption
              color: Color.mOnSurfaceVariant
              font.pixelSize: Math.round(10 * Style.uiScaleRatio)
              elide: Text.ElideRight
            }
          }
        }

        MouseArea {
          id: chipMouse

          anchors.fill: parent
          hoverEnabled: true
          cursorShape: Qt.PointingHandCursor
          onClicked: {
            root.settingsOpen = true;
          }
        }
      }
    }

    Rectangle {
      Layout.fillWidth: true
      Layout.preferredHeight: 1
      color: Color.mOutline
      opacity: 0.75
    }

    ScrollView {
      Layout.fillWidth: true
      Layout.fillHeight: true
      clip: true

      Column {
        width: parent.width
        spacing: Style.marginS

        Rectangle {
          visible: agents.length === 0
          width: parent.width
          height: Math.round(96 * Style.uiScaleRatio)
          radius: Style.radiusL
          color: Style.capsuleColor
          border.color: Style.capsuleBorderColor
          border.width: Style.capsuleBorderWidth

          Text {
            anchors.centerIn: parent
            text: "No tracked agents on this workspace."
            color: Color.mOnSurfaceVariant
            font.pixelSize: Math.round(14 * Style.uiScaleRatio)
          }
        }

        Repeater {
          model: agents

          delegate: Rectangle {
            required property var modelData

            width: parent.width
            height: contentColumn.implicitHeight + (Style.marginM * 2)
            radius: Style.radiusL
            color: modelData.focused ? Qt.lighter(Style.capsuleColor, 1.12) : Style.capsuleColor
            border.color: modelData.focused ? Color.mSecondary : Style.capsuleBorderColor
            border.width: Style.capsuleBorderWidth

            RowLayout {
              anchors.fill: parent
              anchors.margins: Style.marginM
              spacing: Style.marginM

              Item {
                Layout.alignment: Qt.AlignTop
                Layout.preferredWidth: Math.round(30 * Style.uiScaleRatio)
                Layout.preferredHeight: Math.round(30 * Style.uiScaleRatio)

                Image {
                  anchors.fill: parent
                  source: root.iconSourceForTool(String(modelData.tool || ""))
                  fillMode: Image.PreserveAspectFit
                  smooth: true
                  mipmap: true
                }
              }

              ColumnLayout {
                id: contentColumn

                Layout.fillWidth: true
                spacing: Style.margin2XS

                RowLayout {
                  Layout.fillWidth: true
                  spacing: Style.marginS

                  Text {
                    Layout.fillWidth: true
                    text: modelData.label || "Untitled agent"
                    color: Color.mOnSurface
                    font.pixelSize: Math.round(16 * Style.uiScaleRatio)
                    font.bold: true
                    elide: Text.ElideRight
                  }

                  Text {
                    visible: modelData.focused
                    text: "Focused"
                    color: modelData.focused ? Color.mSecondary : Color.mOnSurfaceVariant
                    font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                    font.bold: true
                  }
                }

                Text {
                  Layout.fillWidth: true
                  text: modelData.prompt_first_line || modelData.label || ""
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(14 * Style.uiScaleRatio)
                  wrapMode: Text.WordWrap
                  maximumLineCount: 2
                  elide: Text.ElideRight
                }

                Text {
                  Layout.fillWidth: true
                  visible: !!modelData.project_dir
                  text: modelData.project_dir || ""
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                  font.family: Settings.data.ui.fontFixed
                  elide: Text.ElideMiddle
                }
              }
            }

            MouseArea {
              id: rowMouse
              anchors.fill: parent
              hoverEnabled: true
              cursorShape: Qt.PointingHandCursor
              onClicked: {
                Quickshell.execDetached(["niri", "msg", "action", "focus-window", "--id", String(modelData.window_id)]);
              }
            }
          }
        }
      }
    }
  }

  Item {
    anchors.fill: parent
    z: 50
    visible: opacity > 0.01
    enabled: visible
    opacity: root.settingsOpen ? 1 : 0

    Behavior on opacity {
      NumberAnimation {
        duration: Style.animationNormal
        easing.type: Easing.InOutCubic
      }
    }

    Rectangle {
      anchors.fill: parent
      color: Qt.rgba(0.03, 0.04, 0.05, 0.48)
    }

    MouseArea {
      anchors.fill: parent
      onClicked: {
        if (!providerUpdateProcess.running) {
          root.settingsOpen = false;
        }
      }
    }

    Rectangle {
      id: settingsSheet

      width: Math.min(parent.width - (Style.marginL * 2), Math.round(398 * Style.uiScaleRatio))
      height: settingsContent.implicitHeight + (Style.marginL * 2)
      anchors.horizontalCenter: parent.horizontalCenter
      y: Math.max(Style.marginL, Math.min(parent.height - height - Style.marginL, Math.round((parent.height - height) / 2)))
      radius: Style.radiusL
      color: Qt.lighter(Style.capsuleColor, 1.02)
      border.color: Qt.rgba(1, 1, 1, 0.10)
      border.width: Style.capsuleBorderWidth
      scale: root.settingsOpen ? 1 : 0.96

      Behavior on scale {
        NumberAnimation {
          duration: Style.animationNormal
          easing.type: Easing.InOutCubic
        }
      }

      MouseArea {
        anchors.fill: parent
      }

      ColumnLayout {
        id: settingsContent

        anchors.fill: parent
        anchors.margins: Style.marginL
        spacing: Style.marginM

        Rectangle {
          implicitHeight: headerContent.implicitHeight + (Style.marginM * 2)
          Layout.fillWidth: true
          radius: Style.radiusL
          color: Qt.rgba(1, 1, 1, 0.04)
          border.color: Qt.rgba(1, 1, 1, 0.06)
          border.width: 1

          gradient: Gradient {
            GradientStop { position: 0.0; color: Qt.rgba(0.31, 0.67, 0.55, 0.18) }
            GradientStop { position: 1.0; color: Qt.rgba(0.25, 0.44, 0.82, 0.08) }
          }

          ColumnLayout {
            id: headerContent

            anchors.fill: parent
            anchors.margins: Style.marginM
            spacing: Style.marginS

            RowLayout {
              Layout.fillWidth: true
              spacing: Style.marginS

              Rectangle {
                Layout.preferredWidth: Math.round(34 * Style.uiScaleRatio)
                Layout.preferredHeight: Math.round(34 * Style.uiScaleRatio)
                radius: width / 2
                color: Qt.rgba(1, 1, 1, 0.10)
                border.color: Qt.rgba(1, 1, 1, 0.12)
                border.width: 1

                Text {
                  anchors.centerIn: parent
                  text: "⚙"
                  color: Color.mOnSurface
                  font.pixelSize: Math.round(18 * Style.uiScaleRatio)
                }
              }

              ColumnLayout {
                Layout.fillWidth: true
                spacing: 0

                Text {
                  Layout.fillWidth: true
                  text: "Summary Label Engine"
                  color: Color.mOnSurface
                  font.pixelSize: Math.round(17 * Style.uiScaleRatio)
                  font.bold: true
                  elide: Text.ElideRight
                }

                Text {
                  Layout.fillWidth: true
                  text: "Choose how the extension generates the short task labels shown in the bar and workspace panel."
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                  wrapMode: Text.WordWrap
                }
              }
            }

            Rectangle {
              implicitHeight: activeNowRow.implicitHeight + (Style.marginS * 2)
              Layout.fillWidth: true
              radius: Style.radiusM
              color: Qt.rgba(0, 0, 0, 0.12)
              border.color: Qt.rgba(1, 1, 1, 0.06)
              border.width: 1

              RowLayout {
                id: activeNowRow

                anchors.fill: parent
                anchors.margins: Style.marginS
                spacing: Style.marginS

                Text {
                  text: effectiveSummaryProvider !== "" ? `Active now: ${providerDisplayName(effectiveSummaryProvider)}` : "Active now: No available engine"
                  color: Color.mOnSurface
                  font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                  font.bold: true
                }

                Text {
                  Layout.fillWidth: true
                  horizontalAlignment: Text.AlignRight
                  text: selectedSummaryProvider === "auto" ? "Auto keeps the panel resilient." : "Manual selection uses one engine only."
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(11 * Style.uiScaleRatio)
                  elide: Text.ElideRight
                }
              }
            }
          }
        }

        ColumnLayout {
          Layout.fillWidth: true
          spacing: Style.marginS

          Repeater {
            model: root.providerOptions

            delegate: Rectangle {
              required property var modelData

              implicitHeight: optionContent.implicitHeight + (Style.marginM * 2)
              Layout.fillWidth: true
              radius: Style.radiusL
              color: {
                if (modelData.value === root.selectedSummaryProvider) {
                  return Qt.lighter(Style.capsuleColor, 1.12);
                }
                if (optionMouse.containsMouse && root.providerChoiceEnabled(modelData)) {
                  return Qt.lighter(Style.capsuleColor, 1.08);
                }
                return Style.capsuleColor;
              }
              border.color: modelData.value === root.selectedSummaryProvider ? Color.mSecondary : Style.capsuleBorderColor
              border.width: Style.capsuleBorderWidth
              opacity: root.providerChoiceEnabled(modelData) ? 1 : 0.72

              Behavior on color {
                ColorAnimation {
                  duration: Style.animationNormal
                }
              }

              RowLayout {
                id: optionContent

                anchors.fill: parent
                anchors.margins: Style.marginM
                spacing: Style.marginM

                Rectangle {
                  Layout.alignment: Qt.AlignTop
                  Layout.preferredWidth: Math.round(34 * Style.uiScaleRatio)
                  Layout.preferredHeight: Math.round(34 * Style.uiScaleRatio)
                  radius: width / 2
                  color: root.providerStatusTone(modelData)
                  border.color: Qt.rgba(1, 1, 1, 0.06)
                  border.width: 1

                  Item {
                    anchors.fill: parent

                    Image {
                      anchors.fill: parent
                      anchors.margins: Math.round(7 * Style.uiScaleRatio)
                      source: root.providerUsesLocalImage(String(modelData.value || "")) ? root.providerIconSource(String(modelData.value || "")) : ""
                      fillMode: Image.PreserveAspectFit
                      smooth: true
                      mipmap: true
                      visible: root.providerUsesLocalImage(String(modelData.value || ""))
                    }

                    Text {
                      anchors.centerIn: parent
                      text: "⚙"
                      color: Color.mOnSurface
                      font.pixelSize: Math.round(16 * Style.uiScaleRatio)
                      visible: !root.providerUsesLocalImage(String(modelData.value || ""))
                    }
                  }
                }

                ColumnLayout {
                  Layout.fillWidth: true
                  spacing: Math.round(2 * Style.uiScaleRatio)

                  RowLayout {
                    Layout.fillWidth: true
                    spacing: Style.marginS

                    Text {
                      text: modelData.label || ""
                      color: Color.mOnSurface
                      font.pixelSize: Math.round(15 * Style.uiScaleRatio)
                      font.bold: true
                    }

                    Rectangle {
                      radius: height / 2
                      color: root.providerStatusTone(modelData)
                      border.color: Qt.rgba(1, 1, 1, 0.06)
                      border.width: 1
                      implicitHeight: statusLabel.implicitHeight + 6
                      implicitWidth: statusLabel.implicitWidth + 12

                      Text {
                        id: statusLabel

                        anchors.centerIn: parent
                        text: root.providerSelectionStatus(modelData)
                        color: Color.mOnSurfaceVariant
                        font.pixelSize: Math.round(10 * Style.uiScaleRatio)
                        font.bold: true
                      }
                    }
                  }

                  Text {
                    Layout.fillWidth: true
                    text: modelData.description || ""
                    color: Color.mOnSurfaceVariant
                    font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                    wrapMode: Text.WordWrap
                  }

                  Text {
                    Layout.fillWidth: true
                    text: modelData.detail || ""
                    color: Color.mOnSurfaceVariant
                    font.pixelSize: Math.round(11 * Style.uiScaleRatio)
                    opacity: 0.88
                    wrapMode: Text.WordWrap
                  }
                }
              }

              MouseArea {
                id: optionMouse

                anchors.fill: parent
                enabled: root.providerChoiceEnabled(modelData)
                hoverEnabled: true
                cursorShape: enabled ? Qt.PointingHandCursor : Qt.ForbiddenCursor
                onClicked: {
                  if (String(modelData.value || "") !== root.selectedSummaryProvider) {
                    root.setSummaryProvider(String(modelData.value || ""));
                  } else if (!providerUpdateProcess.running) {
                    root.settingsOpen = false;
                  }
                }
              }
            }
          }
        }

        RowLayout {
          Layout.fillWidth: true
          spacing: Style.marginS

          Text {
            Layout.fillWidth: true
            text: providerUpdateProcess.running ? "Saving your preference and restarting the helper…" : "Changes apply immediately after selection."
            color: Color.mOnSurfaceVariant
            font.pixelSize: Math.round(11 * Style.uiScaleRatio)
            elide: Text.ElideRight
          }

          Rectangle {
            visible: providerUpdateProcess.running
            radius: height / 2
            color: Qt.rgba(0.31, 0.67, 0.55, 0.18)
            implicitHeight: busyLabel.implicitHeight + 8
            implicitWidth: busyLabel.implicitWidth + 14

            Text {
              id: busyLabel

              anchors.centerIn: parent
              text: "Updating"
              color: Color.mOnSurface
              font.pixelSize: Math.round(10 * Style.uiScaleRatio)
              font.bold: true
            }
          }
        }
      }
    }
  }
}
