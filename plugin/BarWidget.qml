import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Widgets
import qs.Commons
import qs.Modules.Bar.Extras
import qs.Services.Compositor
import qs.Widgets

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
  readonly property bool hasFocusedWindow: CompositorService.getFocusedWindow() !== null
  readonly property var activeAgent: {
    if (!hasAgents || !focusedWorkspace)
      return null;

    var activeWindowId = focusedWorkspace.active_window_id;
    var items = focusedWorkspace.agents || [];
    for (var index = 0; index < items.length; index++) {
      var item = items[index];
      if (item && (item.window_id === activeWindowId || item.focused)) {
        return item;
      }
    }
    return null;
  }
  readonly property bool showAgentSummary: !!activeAgent
  readonly property string screenName: screen ? screen.name : ""
  readonly property string barPosition: Settings.getBarPositionForScreen(screenName)
  readonly property bool isVerticalBar: barPosition === "left" || barPosition === "right"
  readonly property real barHeight: Style.getBarHeightForScreen(screenName)
  readonly property real capsuleHeight: Style.getCapsuleHeightForScreen(screenName)
  readonly property real barFontSize: Style.getBarFontSizeForScreen(screenName)
  readonly property real maxWidth: widgetMetadata.maxWidth || 190
  readonly property string fallbackIcon: "user-desktop"
  readonly property string iconSource: {
    var tool = String(activeAgent?.tool || "");
    if (tool === "codex") {
      return pluginApi.pluginDir + "/icons/chatgpt-icon.png";
    }
    if (tool === "claude") {
      return pluginApi.pluginDir + "/icons/claude-ai-icon.png";
    }
    return "";
  }
  readonly property string summaryText: {
    if (showAgentSummary) {
      return activeAgent.label || focusedWorkspace.summary_text || "AI agent";
    }
    return CompositorService.getFocusedWindowTitle() || focusedWorkspace?.active_window_title || "No active window";
  }
  property real mainContentWidth: 0
  readonly property real contentWidth: {
    if (!(showAgentSummary || hasFocusedWindow))
      return 0;

    var iconWidth = badge.width;
    var textWidth = 0;
    var margins = Style.margin2S;

    if (titleContainer.measuredWidth > 0) {
      margins += Style.marginS;
      textWidth = titleContainer.measuredWidth + Style.margin2XXS;
    }

    var countWidth = countBubble.visible ? (countBubble.width + Style.marginS) : 0;
    var total = iconWidth + textWidth + countWidth + margins;
    mainContentWidth = total - textWidth;
    return Math.min(total, maxWidth);
  }

  visible: showAgentSummary || hasFocusedWindow
  Layout.preferredHeight: visible ? (isVerticalBar ? -1 : barHeight) : 0
  Layout.preferredWidth: visible ? (isVerticalBar ? barHeight : -1) : 0
  Layout.fillHeight: false
  Layout.fillWidth: false

  implicitWidth: visible ? (isVerticalBar ? barHeight : contentWidth) : 0
  implicitHeight: visible ? barHeight : 0

  function getAppIcon() {
    try {
      const focusedWindow = CompositorService.getFocusedWindow();
      if (focusedWindow && focusedWindow.appId) {
        const normalizedId = String(focusedWindow.appId).toLowerCase();
        const iconResult = ThemeIcons.iconForAppId(normalizedId);
        if (iconResult && iconResult !== "") {
          return iconResult;
        }
      }

      return ThemeIcons.iconFromName(fallbackIcon);
    } catch (error) {
      Logger.w("AgentStatus", "Error in getAppIcon:", error);
      return ThemeIcons.iconFromName(fallbackIcon);
    }
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
      color: "transparent"
      border.width: 0

      Image {
        anchors.fill: parent
        source: root.iconSource
        fillMode: Image.PreserveAspectFit
        smooth: true
        mipmap: true
        visible: root.showAgentSummary
      }

      IconImage {
        anchors.fill: parent
        source: root.getAppIcon()
        asynchronous: true
        smooth: true
        visible: !root.showAgentSummary && source !== ""
      }
    }

    NScrollText {
      id: titleContainer

      anchors.left: badge.right
      anchors.leftMargin: Style.marginS
      anchors.right: countBubble.visible ? countBubble.left : parent.right
      anchors.rightMargin: Style.marginS
      anchors.verticalCenter: parent.verticalCenter
      height: capsule.height
      text: root.summaryText
      maxWidth: root.maxWidth - root.mainContentWidth
      forcedHover: mouseArea.containsMouse
      scrollMode: NScrollText.ScrollMode.Hover
      cursorShape: Qt.PointingHandCursor
      gradientColor: Style.capsuleColor
      gradientWidth: Math.round(8 * Style.uiScaleRatio)
      cornerRadius: Style.radiusM

      NText {
        color: Color.resolveColorKey(widgetMetadata.textColor || "none")
        pointSize: root.barFontSize
        applyUiScale: false
        font.weight: Style.fontWeightMedium
        elide: Text.ElideNone
      }
    }

    Rectangle {
      id: countBubble

      visible: showAgentSummary && agentCount > 1
      anchors.right: parent.right
      anchors.rightMargin: Style.marginS
      anchors.verticalCenter: parent.verticalCenter
      height: Math.max(16, root.barFontSize + 4)
      width: Math.max(height, countLabel.implicitWidth + 8)
      radius: height / 2
      color: Color.mSurfaceVariant
      border.color: Style.capsuleBorderColor
      border.width: Style.capsuleBorderWidth

      Text {
        id: countLabel

        anchors.centerIn: parent
        text: String(agentCount)
        color: Color.mOnSurfaceVariant
        font.pixelSize: Math.max(10, root.barFontSize - 2)
        font.bold: true
      }
    }

    MouseArea {
      id: mouseArea

      anchors.fill: parent
      hoverEnabled: true
      cursorShape: hasAgents ? Qt.PointingHandCursor : Qt.ArrowCursor
      acceptedButtons: Qt.LeftButton

      onClicked: {
        if (pluginApi && hasAgents) {
          pluginApi.togglePanel(screen, capsule);
        }
      }
    }
  }
}
