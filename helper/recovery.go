package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func parseISOTimestamp(value string) int64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}

	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func slugifyProjectDir(projectDir string) string {
	normalized := strings.TrimSpace(projectDir)
	if normalized == "" {
		return ""
	}
	if normalized == "/" {
		return "-"
	}
	return "-" + strings.ReplaceAll(strings.Trim(normalized, "/"), "/", "-")
}

func extractMessageText(message any) string {
	switch value := message.(type) {
	case string:
		return value
	case map[string]any:
		content, ok := value["content"]
		if !ok {
			return ""
		}
		switch typed := content.(type) {
		case string:
			return typed
		case []any:
			var parts []string
			for _, item := range typed {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if entry["type"] != "text" {
					continue
				}
				text, _ := entry["text"].(string)
				text = strings.TrimSpace(text)
				if text != "" {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

func titleLabel(windowTitle, tool string) string {
	prefix := fmt.Sprintf("AI:%s:", tool)
	if strings.HasPrefix(windowTitle, prefix) {
		label := strings.TrimSpace(strings.TrimPrefix(windowTitle, prefix))
		if label != "" {
			return label
		}
		return strings.Title(tool)
	}

	if strings.Contains(windowTitle, ": ") {
		pathish := strings.TrimSpace(strings.SplitN(windowTitle, ": ", 2)[0])
		if pathish != "" {
			return filepath.Base(strings.Replace(pathish, "~", homeDir, 1))
		}
	}

	if strings.TrimSpace(windowTitle) != "" {
		return strings.TrimSpace(windowTitle)
	}

	return strings.Title(tool)
}

func sqliteQueryJSON[T any](dbPath, sql string) ([]T, error) {
	cmd := exec.Command(sqlite3Binary, "-readonly", "-json", dbPath, sql)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("sqlite3 query failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}

	var rows []T
	if len(bytes.TrimSpace(output)) == 0 {
		return rows, nil
	}

	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func escapeSQL(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func queryCodexThreads(threadIDs []string, cwd string) map[string]recoveredPrompt {
	result := make(map[string]recoveredPrompt)
	if _, err := os.Stat(codexStateDB); err != nil {
		return result
	}

	var sql string
	switch {
	case len(threadIDs) > 0:
		quoted := make([]string, 0, len(threadIDs))
		for _, id := range threadIDs {
			if id == "" {
				continue
			}
			quoted = append(quoted, fmt.Sprintf("'%s'", escapeSQL(id)))
		}
		if len(quoted) == 0 {
			return result
		}
		sql = fmt.Sprintf(`
			SELECT id, cwd, created_at, updated_at, title, first_user_message
			FROM threads
			WHERE id IN (%s)
		`, strings.Join(quoted, ","))
	case cwd != "":
		sql = fmt.Sprintf(`
			SELECT id, cwd, created_at, updated_at, title, first_user_message
			FROM threads
			WHERE cwd = '%s' AND archived = 0
			ORDER BY created_at DESC
			LIMIT 16
		`, escapeSQL(cwd))
	default:
		return result
	}

	rows, err := sqliteQueryJSON[codexThreadRow](codexStateDB, sql)
	if err != nil {
		log.Printf("failed to query codex threads: %v", err)
		return result
	}

	for _, row := range rows {
		prompt := row.FirstUserMessage
		if strings.TrimSpace(prompt) == "" {
			prompt = row.Title
		}
		result[row.ID] = recoveredPrompt{
			ThreadID:        row.ID,
			Cwd:             row.Cwd,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
			PromptFirstLine: normalizePromptLine(prompt, 220),
			Label:           summarizePromptWithTitle(prompt, row.Title, 52),
			MatchKind:       "thread",
		}
	}

	return result
}

func recoverCodexPrompts(agents []detectedAgent) map[int]recoveredPrompt {
	result := make(map[int]recoveredPrompt)
	if len(agents) == 0 {
		return result
	}
	if _, err := os.Stat(codexStateDB); err != nil {
		return result
	}

	patterns := make([]string, 0, len(agents))
	for _, agent := range agents {
		patterns = append(patterns, fmt.Sprintf("process_uuid LIKE 'pid:%d:%%'", agent.AgentPID))
	}

	sql := fmt.Sprintf(`
		SELECT process_uuid, thread_id, MAX(ts) AS last_ts
		FROM logs
		WHERE thread_id != '' AND (%s)
		GROUP BY process_uuid, thread_id
		ORDER BY last_ts DESC
	`, strings.Join(patterns, " OR "))

	rows, err := sqliteQueryJSON[codexLogRow](codexStateDB, sql)
	if err != nil {
		log.Printf("failed to query codex log mappings: %v", err)
		return result
	}

	latestThreadByPID := make(map[int]string)
	for _, row := range rows {
		parts := strings.SplitN(row.ProcessUUID, ":", 3)
		if len(parts) < 3 || parts[0] != "pid" {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		if _, exists := latestThreadByPID[pid]; !exists {
			latestThreadByPID[pid] = row.ThreadID
		}
	}

	threadIDs := make([]string, 0, len(latestThreadByPID))
	for _, threadID := range latestThreadByPID {
		if threadID != "" {
			threadIDs = append(threadIDs, threadID)
		}
	}

	directThreads := queryCodexThreads(threadIDs, "")
	usedThreadIDs := make(map[string]struct{})
	for _, agent := range agents {
		threadID := latestThreadByPID[agent.AgentPID]
		thread, ok := directThreads[threadID]
		if !ok {
			continue
		}
		thread.MatchKind = "direct"
		result[agent.AgentPID] = thread
		usedThreadIDs[threadID] = struct{}{}
	}

	unresolvedByCwd := make(map[string][]detectedAgent)
	for _, agent := range agents {
		if _, ok := result[agent.AgentPID]; ok {
			continue
		}
		unresolvedByCwd[agent.Cwd] = append(unresolvedByCwd[agent.Cwd], agent)
	}

	for cwd, unresolvedAgents := range unresolvedByCwd {
		if cwd == "" {
			continue
		}

		candidateMap := queryCodexThreads(nil, cwd)
		candidates := make([]recoveredPrompt, 0, len(candidateMap))
		for _, candidate := range candidateMap {
			if _, used := usedThreadIDs[candidate.ThreadID]; used {
				continue
			}
			candidates = append(candidates, candidate)
		}
		if len(candidates) == 0 {
			continue
		}

		slices.SortFunc(candidates, func(a, b recoveredPrompt) int {
			return compareInt64(a.CreatedAt, b.CreatedAt)
		})
		slices.SortFunc(unresolvedAgents, func(a, b detectedAgent) int {
			return compareFloat64(a.AgentStartEpoch, b.AgentStartEpoch)
		})

		for _, agent := range unresolvedAgents {
			if len(candidates) == 0 {
				break
			}

			bestIndex := 0
			bestDistance := math.MaxFloat64
			for index, candidate := range candidates {
				distance := math.Abs(float64(candidate.CreatedAt) - agent.AgentStartEpoch)
				if distance < bestDistance {
					bestDistance = distance
					bestIndex = index
				}
			}

			selected := candidates[bestIndex]
			if !isAcceptableInferredMatch(agent.AgentStartEpoch, selected.CreatedAt) {
				continue
			}
			selected.MatchKind = "inferred"
			result[agent.AgentPID] = selected
			usedThreadIDs[selected.ThreadID] = struct{}{}
			candidates = append(candidates[:bestIndex], candidates[bestIndex+1:]...)
		}
	}

	return result
}

func readClaudeSessionFile(sessionPath string) (recoveredPrompt, bool) {
	info, err := os.Stat(sessionPath)
	if err != nil {
		delete(claudeSessionMap, sessionPath)
		return recoveredPrompt{}, false
	}

	if cached, ok := claudeSessionMap[sessionPath]; ok && cached.ModTimeNS == info.ModTime().UnixNano() && cached.Size == info.Size() {
		return cached.Data, true
	}

	file, err := os.Open(sessionPath)
	if err != nil {
		return recoveredPrompt{}, false
	}
	defer file.Close()

	data := recoveredPrompt{
		SessionID: filepath.Base(strings.TrimSuffix(sessionPath, filepath.Ext(sessionPath))),
		UpdatedAt: info.ModTime().Unix(),
	}

	reader := bufio.NewScanner(file)
	reader.Buffer(make([]byte, 1024), 64*1024*1024)

	for reader.Scan() {
		rawLine := strings.TrimSpace(reader.Text())
		if rawLine == "" {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal([]byte(rawLine), &entry); err != nil {
			continue
		}

		if data.ProjectDir == "" {
			if cwd, ok := entry["cwd"].(string); ok {
				data.ProjectDir = cwd
			}
		}

		if timestamp, ok := entry["timestamp"].(string); ok {
			if parsed := parseISOTimestamp(timestamp); parsed > data.UpdatedAt {
				data.UpdatedAt = parsed
			}
		}

		if entry["type"] == "last-prompt" {
			if lastPrompt, ok := entry["lastPrompt"].(string); ok {
				normalized := normalizePromptLine(lastPrompt, 220)
				if normalized != "" {
					data.LastPrompt = normalized
				}
			}
			continue
		}

		if data.PromptFirstLine != "" {
			continue
		}
		if entry["type"] != "user" {
			continue
		}
		if parentUUID, exists := entry["parentUuid"]; exists && parentUUID != nil {
			continue
		}

		promptText := normalizePromptLine(extractMessageText(entry["message"]), 220)
		if promptText == "" {
			continue
		}

		data.PromptFirstLine = promptText
		data.Label = summarizePrompt(promptText, 52)
		if timestamp, ok := entry["timestamp"].(string); ok {
			data.CreatedAt = parseISOTimestamp(timestamp)
		}
	}

	if err := reader.Err(); err != nil && err != io.EOF {
		return recoveredPrompt{}, false
	}

	if data.PromptFirstLine == "" && data.LastPrompt != "" {
		data.PromptFirstLine = data.LastPrompt
		data.Label = summarizePrompt(data.LastPrompt, 52)
	}

	claudeSessionMap[sessionPath] = claudeSessionCache{
		ModTimeNS: info.ModTime().UnixNano(),
		Size:      info.Size(),
		Data:      data,
	}
	return data, true
}

func loadClaudeSessions(projectDir string) []recoveredPrompt {
	slug := slugifyProjectDir(projectDir)
	if slug == "" {
		return nil
	}

	projectPath := filepath.Join(claudeProjects, slug)
	info, err := os.Stat(projectPath)
	if err != nil || !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return nil
	}

	var sessions []recoveredPrompt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		session, ok := readClaudeSessionFile(filepath.Join(projectPath, entry.Name()))
		if !ok {
			continue
		}
		if session.ProjectDir != "" && session.ProjectDir != projectDir {
			continue
		}
		sessions = append(sessions, session)
	}

	slices.SortFunc(sessions, func(a, b recoveredPrompt) int {
		return compareInt64(a.CreatedAt, b.CreatedAt)
	})
	return sessions
}

func recoverClaudePrompts(agents []detectedAgent) map[int]recoveredPrompt {
	result := make(map[int]recoveredPrompt)
	if len(agents) == 0 {
		return result
	}

	agentsByCwd := make(map[string][]detectedAgent)
	for _, agent := range agents {
		agentsByCwd[agent.Cwd] = append(agentsByCwd[agent.Cwd], agent)
	}

	for cwd, cwdAgents := range agentsByCwd {
		if cwd == "" {
			continue
		}

		sessions := loadClaudeSessions(cwd)
		if len(sessions) == 0 {
			continue
		}

		slices.SortFunc(cwdAgents, func(a, b detectedAgent) int {
			return compareFloat64(a.AgentStartEpoch, b.AgentStartEpoch)
		})
		available := append([]recoveredPrompt(nil), sessions...)

		for _, agent := range cwdAgents {
			if len(available) == 0 {
				break
			}

			bestIndex := 0
			bestDistance := math.MaxFloat64
			for index, session := range available {
				distance := math.Abs(float64(session.CreatedAt) - agent.AgentStartEpoch)
				if distance < bestDistance {
					bestDistance = distance
					bestIndex = index
				}
			}

			selected := available[bestIndex]
			if !isAcceptableInferredMatch(agent.AgentStartEpoch, selected.CreatedAt) {
				continue
			}

			selected.MatchKind = "inferred"
			result[agent.AgentPID] = selected
			available = append(available[:bestIndex], available[bestIndex+1:]...)
		}
	}

	return result
}

func buildRecoveredPrompts(agents []detectedAgent) map[int]recoveredPrompt {
	result := make(map[int]recoveredPrompt)
	var codexAgents []detectedAgent
	var claudeAgents []detectedAgent

	for _, agent := range agents {
		switch agent.Tool {
		case "codex":
			codexAgents = append(codexAgents, agent)
		case "claude":
			claudeAgents = append(claudeAgents, agent)
		}
	}

	for pid, prompt := range recoverCodexPrompts(codexAgents) {
		result[pid] = prompt
	}
	for pid, prompt := range recoverClaudePrompts(claudeAgents) {
		result[pid] = prompt
	}
	return result
}
