import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import qs.Commons

Item {
  id: root

  property ShellScreen screen
  property string widgetId: ""
  property string section: ""
  property int sectionWidgetIndex: -1
  property int sectionWidgetsCount: 0
  property var pluginApi

  readonly property var widgetMetadata: pluginApi?.manifest?.metadata || {}
  readonly property var mainState: pluginApi?.mainInstance
  readonly property var focusedWorkspace: mainState ? mainState.focusedWorkspace : null
  readonly property bool hasAgents: !!(focusedWorkspace && focusedWorkspace.agent_count > 0)
  readonly property int agentCount: focusedWorkspace ? (focusedWorkspace.agent_count || 0) : 0
  readonly property string screenName: screen ? screen.name : ""
  readonly property string barPosition: Settings.getBarPositionForScreen(screenName)
  readonly property bool isVerticalBar: barPosition === "left" || barPosition === "right"
  readonly property real barHeight: Style.getBarHeightForScreen(screenName)
  readonly property real capsuleHeight: Style.getCapsuleHeightForScreen(screenName)
  readonly property real maxWidth: widgetMetadata.maxWidth || 190
  readonly property string summaryText: {
    if (hasAgents) {
      return focusedWorkspace.summary_text || "AI agent";
    }
    if (focusedWorkspace && focusedWorkspace.active_window_title) {
      return focusedWorkspace.active_window_title;
    }
    return "No active window";
  }
  readonly property string tooltipText: {
    if (hasAgents) {
      var promptLine = focusedWorkspace.primary_prompt_line || summaryText;
      return agentCount > 1 ? `${promptLine}\n${agentCount} agents on workspace ${focusedWorkspace.name || focusedWorkspace.idx}` : promptLine;
    }
    return summaryText;
  }
  readonly property real contentWidth: Math.min(maxWidth, textMetrics.advanceWidth + leftPadding + rightPadding + (agentCount > 1 ? countBubble.width + Style.marginS : 0))
  readonly property int leftPadding: Style.marginS + badge.width + Style.marginS
  readonly property int rightPadding: Style.marginS

  Layout.preferredHeight: isVerticalBar ? -1 : barHeight
  Layout.preferredWidth: isVerticalBar ? barHeight : -1
  Layout.fillHeight: false
  Layout.fillWidth: false

  implicitWidth: isVerticalBar ? barHeight : contentWidth
  implicitHeight: isVerticalBar ? barHeight : barHeight

  TextMetrics {
    id: textMetrics
    font.pixelSize: Style.getBarFontSizeForScreen(screenName)
    text: root.summaryText
  }

  Rectangle {
    id: capsule

    x: Style.pixelAlignCenter(parent.width, width)
    y: Style.pixelAlignCenter(parent.height, height)
    width: Style.toOdd(isVerticalBar ? barHeight : contentWidth)
    height: Style.toOdd(isVerticalBar ? barHeight : capsuleHeight)
    radius: Style.radiusM
    color: Style.capsuleColor
    border.color: Style.capsuleBorderColor
    border.width: Style.capsuleBorderWidth

    Behavior on width {
      NumberAnimation {
        duration: Style.animationNormal
        easing.type: Easing.InOutCubic
      }
    }

    Rectangle {
      id: badge

      width: Style.toOdd(capsule.height * 0.6)
      height: width
      radius: width / 2
      x: Style.marginS
      y: Style.pixelAlignCenter(parent.height, height)
      color: hasAgents ? Color.mPrimary : Color.mSurfaceVariant

      Text {
        anchors.centerIn: parent
        text: "AI"
        color: hasAgents ? Color.mOnPrimary : Color.mOnSurfaceVariant
        font.pixelSize: Math.max(10, Style.getBarFontSizeForScreen(screenName) - 1)
        font.bold: true
      }
    }

    Text {
      id: summaryLabel

      anchors.left: badge.right
      anchors.leftMargin: Style.marginS
      anchors.right: countBubble.visible ? countBubble.left : parent.right
      anchors.rightMargin: Style.marginS
      anchors.verticalCenter: parent.verticalCenter
      text: root.summaryText
      color: Color.resolveColorKey(widgetMetadata.textColor || "none")
      font.pixelSize: Style.getBarFontSizeForScreen(screenName)
      elide: Text.ElideRight
      verticalAlignment: Text.AlignVCenter
    }

    Rectangle {
      id: countBubble

      visible: hasAgents && agentCount > 1
      anchors.right: parent.right
      anchors.rightMargin: Style.marginS
      anchors.verticalCenter: parent.verticalCenter
      height: Math.max(16, summaryLabel.font.pixelSize + 4)
      width: Math.max(height, countLabel.implicitWidth + 8)
      radius: height / 2
      color: Color.mSecondary

      Text {
        id: countLabel

        anchors.centerIn: parent
        text: String(agentCount)
        color: Color.mOnSurface
        font.pixelSize: Math.max(10, Style.getBarFontSizeForScreen(screenName) - 2)
        font.bold: true
      }
    }

    MouseArea {
      id: mouseArea

      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      acceptedButtons: Qt.LeftButton

      onClicked: {
        if (pluginApi) {
          pluginApi.togglePanel(screen, capsule);
        }
      }
    }

    ToolTip.visible: mouseArea.containsMouse
    ToolTip.delay: 300
    ToolTip.text: root.tooltipText
  }
}
