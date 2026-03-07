import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import qs.Commons

Item {
  id: root

  property var pluginApi

  property bool allowAttach: true
  property real contentPreferredWidth: Math.round(460 * Style.uiScaleRatio)
  property real contentPreferredHeight: Math.round(Math.min(maxPanelHeight, Math.max(200, 124 + (agents.length * 92 * Style.uiScaleRatio))))

  readonly property var mainState: pluginApi?.mainInstance
  readonly property var focusedWorkspace: mainState ? mainState.focusedWorkspace : null
  readonly property var agents: focusedWorkspace ? (focusedWorkspace.agents || []) : []
  readonly property real maxPanelHeight: ((pluginApi?.panelOpenScreen?.height || 900) * 0.72)

  ColumnLayout {
    x: Style.marginL
    y: Style.marginL
    width: parent.width - Style.margin2L
    height: parent.height - Style.margin2L
    spacing: Style.marginM

    ColumnLayout {
      Layout.fillWidth: true
      spacing: Style.margin2XS

      Text {
        Layout.fillWidth: true
        text: focusedWorkspace ? `Workspace ${focusedWorkspace.name || focusedWorkspace.idx}` : "Workspace agents"
        color: Color.mOnSurface
        font.pixelSize: Math.round(16 * Style.uiScaleRatio)
        font.bold: true
        elide: Text.ElideRight
      }

      Text {
        Layout.fillWidth: true
        text: agents.length > 0 ? `${agents.length} tracked agent${agents.length === 1 ? "" : "s"}` : "No tracked agents on this workspace"
        color: Color.mOnSurfaceVariant
        font.pixelSize: Math.round(12 * Style.uiScaleRatio)
        elide: Text.ElideRight
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
            font.pixelSize: Math.round(13 * Style.uiScaleRatio)
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

              Rectangle {
                Layout.alignment: Qt.AlignTop
                Layout.preferredWidth: Math.round(72 * Style.uiScaleRatio)
                Layout.preferredHeight: Math.round(26 * Style.uiScaleRatio)
                radius: height / 2
                color: modelData.tool === "codex" ? Color.mPrimary : Color.mSecondary

                Text {
                  anchors.centerIn: parent
                  text: String(modelData.tool_display || modelData.tool || "agent").toUpperCase()
                  color: modelData.tool === "codex" ? Color.mOnPrimary : Color.mOnSurface
                  font.pixelSize: Math.round(11 * Style.uiScaleRatio)
                  font.bold: true
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
                    font.pixelSize: Math.round(14 * Style.uiScaleRatio)
                    font.bold: true
                    elide: Text.ElideRight
                  }

                  Text {
                    visible: modelData.focused
                    text: "Focused"
                    color: modelData.focused ? Color.mSecondary : Color.mOnSurfaceVariant
                    font.pixelSize: Math.round(11 * Style.uiScaleRatio)
                    font.bold: true
                  }
                }

                Text {
                  Layout.fillWidth: true
                  text: modelData.prompt_first_line || modelData.label || ""
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(12 * Style.uiScaleRatio)
                  wrapMode: Text.WordWrap
                  maximumLineCount: 2
                  elide: Text.ElideRight
                }

                Text {
                  Layout.fillWidth: true
                  visible: !!modelData.project_dir
                  text: modelData.project_dir || ""
                  color: Color.mOnSurfaceVariant
                  font.pixelSize: Math.round(11 * Style.uiScaleRatio)
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

            ToolTip.visible: rowMouse.containsMouse
            ToolTip.delay: 300
            ToolTip.text: modelData.window_title || modelData.label || ""
          }
        }
      }
    }
  }
}
