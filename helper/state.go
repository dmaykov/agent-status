package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

func finalizeAgent(agent detectedAgent, recovered map[int]recoveredPrompt) finalAgent {
	metadata := parseSessionFile(agent.WindowPID)
	recoveredData, hasRecovered := recovered[agent.AgentPID]
	freshAgent := isFreshAgent(agent)
	usableRecovered := hasRecovered && canUseRecoveredPrompt(agent, recoveredData)

	projectDir := metadata["project_dir"]
	if projectDir == "" && usableRecovered {
		projectDir = recoveredData.ProjectDir
	}
	if projectDir == "" {
		projectDir = agent.Cwd
	}

	label := metadata["label"]
	if label == "" && usableRecovered {
		label = recoveredData.Label
	}
	if label == "" && shouldUsePlaceholder(freshAgent, metadata, usableRecovered) {
		label = "Starting…"
	}
	if label == "" {
		label = titleLabel(agent.WindowTitle, agent.Tool)
	}

	promptFirstLine := metadata["prompt_first_line"]
	if promptFirstLine == "" && usableRecovered {
		promptFirstLine = recoveredData.PromptFirstLine
	}
	if promptFirstLine == "" && label == "Starting…" {
		promptFirstLine = "Waiting for prompt…"
	}
	if promptFirstLine == "" {
		promptFirstLine = label
	}

	return finalAgent{
		WindowID:        agent.WindowID,
		WorkspaceID:     agent.WorkspaceID,
		Tool:            agent.Tool,
		ToolDisplay:     agent.ToolDisplay,
		Label:           label,
		PromptFirstLine: promptFirstLine,
		ProjectDir:      projectDir,
		WindowTitle:     agent.WindowTitle,
		Focused:         agent.Focused,
		Position:        agent.Position,
		FocusOrder:      agent.FocusOrder,
	}
}

func isAcceptableInferredMatch(agentStartEpoch float64, createdAt int64) bool {
	if createdAt <= 0 || agentStartEpoch <= 0 {
		return false
	}

	delta := time.Duration(float64(time.Second) * (float64(createdAt) - agentStartEpoch))
	return delta >= -inferredPromptMaxLead && delta <= inferredPromptMaxLag
}

func isReliableRecoveredPrompt(prompt recoveredPrompt) bool {
	return prompt.MatchKind == "direct" || prompt.MatchKind == "metadata"
}

func canUseRecoveredPrompt(agent detectedAgent, prompt recoveredPrompt) bool {
	if isReliableRecoveredPrompt(prompt) {
		return true
	}
	if prompt.MatchKind != "inferred" {
		return false
	}
	if !isFreshAgent(agent) {
		return true
	}
	return isFreshRecoveredPrompt(agent, prompt)
}

func isFreshAgent(agent detectedAgent) bool {
	if agent.AgentStartEpoch <= 0 {
		return false
	}
	age := time.Since(time.Unix(int64(agent.AgentStartEpoch), 0))
	return age >= 0 && age <= freshAgentPlaceholderAge
}

func isFreshRecoveredPrompt(agent detectedAgent, prompt recoveredPrompt) bool {
	if agent.AgentStartEpoch <= 0 || prompt.CreatedAt <= 0 {
		return false
	}
	delta := time.Duration(float64(time.Second) * (float64(prompt.CreatedAt) - agent.AgentStartEpoch))
	return delta >= -freshInferredMaxLead && delta <= freshInferredMaxLag
}

func shouldUsePlaceholder(freshAgent bool, metadata map[string]string, usableRecovered bool) bool {
	if len(metadata) > 0 || usableRecovered {
		return false
	}
	return freshAgent
}

func buildState(windows []window, workspaces []workspace) (state, error) {
	processes, children := readProcTable()
	windowsByID := make(map[int64]window, len(windows))
	agentsByWorkspace := make(map[int64][]finalAgent)
	var detectedAgents []detectedAgent

	for _, win := range windows {
		windowsByID[win.ID] = win
		if win.AppID != "Alacritty" {
			continue
		}
		agent := detectAgent(win, processes, children)
		if agent != nil {
			detectedAgents = append(detectedAgents, *agent)
		}
	}

	recoveredPrompts := buildRecoveredPrompts(detectedAgents)
	for _, agent := range detectedAgents {
		if agent.WorkspaceID < 0 {
			continue
		}
		agentsByWorkspace[agent.WorkspaceID] = append(agentsByWorkspace[agent.WorkspaceID], finalizeAgent(agent, recoveredPrompts))
	}

	slices.SortFunc(workspaces, func(a, b workspace) int {
		return compareInt64(int64(a.Idx), int64(b.Idx))
	})

	var workspaceStates []workspaceState
	var focusedWorkspace *workspaceState

	for _, ws := range workspaces {
		activeWindow := windowsByID[ws.ActiveWindowID]
		activeWindowTitle := activeWindow.Title
		if strings.TrimSpace(activeWindowTitle) == "" {
			activeWindowTitle = "No active window"
		}

		agents := append([]finalAgent(nil), agentsByWorkspace[ws.ID]...)
		slices.SortFunc(agents, compareFinalAgents)

		summaryText := activeWindowTitle
		primaryPromptLine := ""
		if len(agents) > 0 {
			summaryText = agents[0].Label
			if len(agents) > 1 {
				summaryText = fmt.Sprintf("%s +%d", summaryText, len(agents)-1)
			}
			primaryPromptLine = firstNonEmpty(agents[0].PromptFirstLine, agents[0].Label)
		}

		stateEntry := workspaceState{
			ID:                ws.ID,
			Idx:               ws.Idx,
			Name:              firstNonEmpty(ws.Name, strconv.Itoa(max(1, ws.Idx))),
			IsFocused:         ws.IsFocused,
			ActiveWindowID:    ws.ActiveWindowID,
			ActiveWindowTitle: activeWindowTitle,
			SummaryText:       summaryText,
			PrimaryPromptLine: primaryPromptLine,
			AgentCount:        len(agents),
			Agents:            agents,
		}
		workspaceStates = append(workspaceStates, stateEntry)

		if stateEntry.IsFocused {
			copyEntry := stateEntry
			focusedWorkspace = &copyEntry
		}
	}

	if focusedWorkspace == nil && len(workspaceStates) > 0 {
		for index := range workspaceStates {
			if workspaceStates[index].AgentCount > 0 {
				copyEntry := workspaceStates[index]
				focusedWorkspace = &copyEntry
				break
			}
		}
		if focusedWorkspace == nil {
			copyEntry := workspaceStates[0]
			focusedWorkspace = &copyEntry
		}
	}

	return state{
		GeneratedAt:      time.Now().Format(defaultStateTimestampFmt),
		FocusedWorkspace: focusedWorkspace,
		SummaryProvider:  currentSummaryProviderState(),
		Workspaces:       workspaceStates,
	}, nil
}

func compareFinalAgents(a, b finalAgent) int {
	if a.Focused != b.Focused {
		if a.Focused {
			return -1
		}
		return 1
	}
	if a.Position[0] != b.Position[0] {
		return compareInt(a.Position[0], b.Position[0])
	}
	if a.Position[1] != b.Position[1] {
		return compareInt(a.Position[1], b.Position[1])
	}
	if a.FocusOrder[0] != b.FocusOrder[0] {
		return compareInt64(b.FocusOrder[0], a.FocusOrder[0])
	}
	return compareInt64(b.FocusOrder[1], a.FocusOrder[1])
}

func writeState(current state, previousPayload string) (string, error) {
	payloadBytes, err := json.Marshal(current)
	if err != nil {
		return previousPayload, err
	}
	payload := string(payloadBytes)
	if payload == previousPayload {
		return payload, nil
	}

	formatted, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return previousPayload, err
	}
	formatted = append(formatted, '\n')

	tempPath := stateFile + ".tmp"
	if err := os.WriteFile(tempPath, formatted, 0o644); err != nil {
		return previousPayload, err
	}
	if err := os.Rename(tempPath, stateFile); err != nil {
		return previousPayload, err
	}
	return payload, nil
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareFloat64(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
