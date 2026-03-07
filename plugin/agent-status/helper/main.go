package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"
)

const (
	defaultTickRate          = 100
	defaultStateTimestampFmt = "2006-01-02T15:04:05-0700"
	eventRefreshInterval     = 75 * time.Millisecond
	fallbackResyncInterval   = 15 * time.Second
	eventStreamRestartDelay  = 2 * time.Second
	maxEventStreamLineBytes  = 1024 * 1024
	aiSummaryTimeout         = 25 * time.Second
	aiSummaryRetryDelay      = 15 * time.Minute
)

var (
	homeDir          = getenv("HOME", mustHomeDir())
	cacheRoot        = filepath.Join(getenv("XDG_CACHE_HOME", filepath.Join(homeDir, ".cache")), "agent-status")
	stateFile        = filepath.Join(cacheRoot, "state.json")
	sessionsDir      = filepath.Join(cacheRoot, "sessions")
	summaryCacheFile = filepath.Join(cacheRoot, "summary-cache.json")
	codexStateDB     = filepath.Join(homeDir, ".codex", "state_5.sqlite")
	claudeProjects   = filepath.Join(homeDir, ".claude", "projects")
	agentNames       = map[string]struct{}{"codex": {}, "claude": {}}
	bootTime         = readBootTime()
	clockTicks       = readClockTicks()
	sqlite3Binary    = findBinary("sqlite3")
	codexBinary      = findBinary("codex")
	claudeSessionMap = map[string]claudeSessionCache{}
	summaryCache     = map[string]summaryCacheEntry{}
	summaryFailures  = map[string]time.Time{}
	summaryInFlight  = map[string]struct{}{}
	summaryQueue     = make(chan summaryRequest, 64)
	summaryRefreshCh = make(chan struct{}, 1)
	summaryCacheMu   sync.RWMutex
)

type window struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	AppID       string `json:"app_id"`
	PID         int    `json:"pid"`
	WorkspaceID int64  `json:"workspace_id"`
	IsFocused   bool   `json:"is_focused"`
	Layout      struct {
		PosInScrollingLayout []int `json:"pos_in_scrolling_layout"`
	} `json:"layout"`
	FocusTimestamp struct {
		Secs  int64 `json:"secs"`
		Nanos int64 `json:"nanos"`
	} `json:"focus_timestamp"`
}

type workspace struct {
	ID             int64  `json:"id"`
	Idx            int    `json:"idx"`
	Name           string `json:"name"`
	IsFocused      bool   `json:"is_focused"`
	ActiveWindowID int64  `json:"active_window_id"`
}

type procInfo struct {
	PID        int
	PPID       int
	StartTicks int64
	Cmdline    []string
	ExeName    string
	CmdText    string
	Cwd        string
}

type detectedAgent struct {
	WindowID        int64
	WindowPID       int
	WorkspaceID     int64
	Tool            string
	ToolDisplay     string
	AgentPID        int
	Cwd             string
	AgentStartEpoch float64
	WindowTitle     string
	Focused         bool
	Position        [2]int
	FocusOrder      [2]int64
}

type recoveredPrompt struct {
	ThreadID        string
	SessionID       string
	Cwd             string
	ProjectDir      string
	CreatedAt       int64
	UpdatedAt       int64
	Label           string
	PromptFirstLine string
	LastPrompt      string
}

type finalAgent struct {
	WindowID        int64    `json:"window_id"`
	WorkspaceID     int64    `json:"workspace_id"`
	Tool            string   `json:"tool"`
	ToolDisplay     string   `json:"tool_display"`
	Label           string   `json:"label"`
	PromptFirstLine string   `json:"prompt_first_line"`
	ProjectDir      string   `json:"project_dir"`
	WindowTitle     string   `json:"window_title"`
	Focused         bool     `json:"focused"`
	Position        [2]int   `json:"position"`
	FocusOrder      [2]int64 `json:"focus_order"`
}

type workspaceState struct {
	ID                int64        `json:"id"`
	Idx               int          `json:"idx"`
	Name              string       `json:"name"`
	IsFocused         bool         `json:"is_focused"`
	ActiveWindowID    int64        `json:"active_window_id"`
	ActiveWindowTitle string       `json:"active_window_title"`
	SummaryText       string       `json:"summary_text"`
	PrimaryPromptLine string       `json:"primary_prompt_line"`
	AgentCount        int          `json:"agent_count"`
	Agents            []finalAgent `json:"agents"`
}

type state struct {
	GeneratedAt      string           `json:"generated_at"`
	FocusedWorkspace *workspaceState  `json:"focused_workspace"`
	Workspaces       []workspaceState `json:"workspaces"`
}

type codexLogRow struct {
	ProcessUUID string `json:"process_uuid"`
	ThreadID    string `json:"thread_id"`
	LastTS      int64  `json:"last_ts"`
}

type codexThreadRow struct {
	ID               string `json:"id"`
	Cwd              string `json:"cwd"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
	Title            string `json:"title"`
	FirstUserMessage string `json:"first_user_message"`
}

type claudeSessionCache struct {
	ModTimeNS int64
	Size      int64
	Data      recoveredPrompt
}

type summaryCacheEntry struct {
	Summary   string `json:"summary"`
	UpdatedAt int64  `json:"updated_at"`
}

type summaryRequest struct {
	CacheKey string
	Prompt   string
	Title    string
}

func main() {
	log.SetFlags(0)
	if err := ensureDirs(); err != nil {
		log.Printf("failed to ensure cache dirs: %v", err)
		os.Exit(1)
	}
	loadSummaryCache()
	go runSummaryWorker()

	var lastPayload string
	refresh := func() {
		payload, err := refreshState(lastPayload)
		if err != nil {
			log.Printf("failed to refresh state: %v", err)
			return
		}
		lastPayload = payload
	}

	refresh()
	for {
		if err := watchEventStream(&lastPayload); err != nil {
			log.Printf("event stream stopped: %v", err)
		}
		refresh()
		time.Sleep(eventStreamRestartDelay)
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustHomeDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return home
	}
	return "/tmp"
}

func readClockTicks() int64 {
	output, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return defaultTickRate
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || value <= 0 {
		return defaultTickRate
	}
	return value
}

func readBootTime() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return float64(time.Now().Unix())
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 64)
			if err == nil {
				return value
			}
		}
	}

	return float64(time.Now().Unix())
}

func findBinary(name string) string {
	if binary, err := exec.LookPath(name); err == nil {
		return binary
	}
	return name
}

func ensureDirs() error {
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(sessionsDir, 0o755)
}

func fetchWindows() ([]window, error) {
	var windows []window
	if err := runJSON([]string{"niri", "msg", "-j", "windows"}, &windows); err != nil {
		return nil, err
	}
	return windows, nil
}

func fetchWorkspaces() ([]workspace, error) {
	var workspaces []workspace
	if err := runJSON([]string{"niri", "msg", "-j", "workspaces"}, &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func refreshState(previousPayload string) (string, error) {
	cleanupSessions()

	windows, err := fetchWindows()
	if err != nil {
		return previousPayload, err
	}

	workspaces, err := fetchWorkspaces()
	if err != nil {
		return previousPayload, err
	}

	currentState, err := buildState(windows, workspaces)
	if err != nil {
		return previousPayload, err
	}

	return writeState(currentState, previousPayload)
}

func watchEventStream(lastPayload *string) error {
	cmd := exec.Command("niri", "msg", "-j", "event-stream")
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	eventCh := make(chan struct{}, 1)
	scanErrCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), maxEventStreamLineBytes)
		for scanner.Scan() {
			select {
			case eventCh <- struct{}{}:
			default:
			}
		}

		if err := scanner.Err(); err != nil {
			scanErrCh <- err
			return
		}
		scanErrCh <- io.EOF
	}()

	refresh := func() time.Time {
		refreshedAt := time.Now()
		payload, err := refreshState(*lastPayload)
		if err != nil {
			log.Printf("failed to refresh state: %v", err)
			return refreshedAt
		}
		*lastPayload = payload
		return refreshedAt
	}

	fallbackTicker := time.NewTicker(fallbackResyncInterval)
	defer fallbackTicker.Stop()

	debounceTimer := time.NewTimer(time.Hour)
	stopTimer(debounceTimer)

	lastRefresh := time.Time{}
	for {
		select {
		case <-eventCh:
			elapsed := time.Since(lastRefresh)
			if lastRefresh.IsZero() || elapsed >= eventRefreshInterval {
				stopTimer(debounceTimer)
				lastRefresh = refresh()
				continue
			}
			resetTimer(debounceTimer, eventRefreshInterval-elapsed)
		case <-debounceTimer.C:
			lastRefresh = refresh()
		case <-fallbackTicker.C:
			lastRefresh = refresh()
		case <-summaryRefreshCh:
			lastRefresh = refresh()
		case err := <-scanErrCh:
			waitErr := cmd.Wait()
			switch {
			case waitErr != nil:
				return waitErr
			case err == io.EOF:
				return fmt.Errorf("niri event-stream closed")
			default:
				return err
			}
		}
	}
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	stopTimer(timer)
	timer.Reset(delay)
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func runJSON(command []string, out any) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}

	cmd := exec.Command(command[0], command[1:]...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("%s: %s", strings.Join(command, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("%s: %w", strings.Join(command, " "), err)
	}

	if err := json.Unmarshal(output, out); err != nil {
		return fmt.Errorf("invalid json from %s: %w", strings.Join(command, " "), err)
	}
	return nil
}

func readProcTable() (map[int]procInfo, map[int][]int) {
	processes := make(map[int]procInfo)
	children := make(map[int][]int)

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processes, children
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		statData, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}

		statText := string(statData)
		rparen := strings.LastIndex(statText, ")")
		if rparen == -1 || rparen+2 >= len(statText) {
			continue
		}

		fields := strings.Fields(statText[rparen+2:])
		if len(fields) < 20 {
			continue
		}

		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		startTicks, err := strconv.ParseInt(fields[19], 10, 64)
		if err != nil {
			startTicks = 0
		}

		cmdlineData, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		cmdline := splitCmdline(cmdlineData)
		if err != nil {
			cmdline = nil
		}

		cwd, _ := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		exeName := ""
		if len(cmdline) > 0 {
			exeName = filepath.Base(cmdline[0])
		}

		process := procInfo{
			PID:        pid,
			PPID:       ppid,
			StartTicks: startTicks,
			Cmdline:    cmdline,
			ExeName:    exeName,
			CmdText:    strings.Join(cmdline, " "),
			Cwd:        cwd,
		}
		processes[pid] = process
		children[ppid] = append(children[ppid], pid)
	}

	return processes, children
}

func splitCmdline(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		result = append(result, string(part))
	}
	return result
}

func walkDescendants(pid int, children map[int][]int) []int {
	queue := []int{pid}
	seen := make(map[int]struct{})
	var ordered []int

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		ordered = append(ordered, current)
		queue = append(queue, children[current]...)
	}

	return ordered
}

func parseSessionFile(pid int) map[string]string {
	sessionPath := filepath.Join(sessionsDir, fmt.Sprintf("%d.session", pid))
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return map[string]string{}
	}

	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "\t") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		result[parts[0]] = parts[1]
	}
	return result
}

func cleanupSessions() {
	entries, err := filepath.Glob(filepath.Join(sessionsDir, "*.session"))
	if err != nil {
		return
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(entry), ".session"))
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
			continue
		}
		_ = os.Remove(entry)
	}
}

func processStartEpoch(proc procInfo) float64 {
	if proc.StartTicks <= 0 {
		return 0
	}
	return bootTime + float64(proc.StartTicks)/float64(clockTicks)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max(0, limit-1)])) + "…"
}

func normalizePromptText(text string, limit int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimLeft(line, "›>-*•"))
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return truncateRunes(strings.Join(strings.Fields(strings.Join(parts, " ")), " "), limit)
}

func normalizePromptLine(text string, limit int) string {
	line := strings.TrimSpace(strings.TrimLeft(firstNonEmptyLine(text), "›>"))
	return truncateRunes(strings.Join(strings.Fields(line), " "), limit)
}

func summarizePrompt(text string, limit int) string {
	return summarizePromptWithTitle(text, "", limit)
}

func summarizePromptWithTitle(prompt, title string, limit int) string {
	if summary := cachedAISummary(prompt, title); isUsefulSummary(summary) {
		return truncateRunes(summary, limit)
	}
	queueAISummary(prompt, title)

	titleSummary := buildPromptSummary(title, limit)
	promptSummary := buildPromptSummary(prompt, limit)

	if isUsefulSummary(promptSummary) && shouldPreferPromptSummary(titleSummary, promptSummary) {
		return promptSummary
	}
	if isUsefulSummary(titleSummary) {
		return titleSummary
	}
	if promptSummary != "" {
		return promptSummary
	}
	if title != "" {
		return truncateRunes(normalizePromptLine(title, limit), limit)
	}
	return truncateRunes(normalizePromptLine(prompt, limit), limit)
}

func cachedAISummary(prompt, title string) string {
	normalizedPrompt := normalizePromptText(prompt, 1200)
	normalizedTitle := normalizePromptText(title, 240)
	if normalizedPrompt == "" && normalizedTitle == "" {
		return ""
	}

	cacheKey := summaryCacheKey(normalizedTitle, normalizedPrompt)
	return cachedSummary(cacheKey)
}

func summaryCacheKey(title, prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(title) + "\n---\n" + strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}

func cachedSummary(cacheKey string) string {
	summaryCacheMu.RLock()
	defer summaryCacheMu.RUnlock()
	entry, ok := summaryCache[cacheKey]
	if !ok {
		return ""
	}
	return strings.TrimSpace(entry.Summary)
}

func summaryFailedRecently(cacheKey string) bool {
	summaryCacheMu.RLock()
	defer summaryCacheMu.RUnlock()
	lastFailure, ok := summaryFailures[cacheKey]
	return ok && time.Since(lastFailure) < aiSummaryRetryDelay
}

func recordSummaryFailure(cacheKey string) {
	summaryCacheMu.Lock()
	defer summaryCacheMu.Unlock()
	summaryFailures[cacheKey] = time.Now()
}

func clearSummaryFailure(cacheKey string) {
	summaryCacheMu.Lock()
	defer summaryCacheMu.Unlock()
	delete(summaryFailures, cacheKey)
}

func queueAISummary(prompt, title string) {
	normalizedPrompt := normalizePromptText(prompt, 1200)
	normalizedTitle := normalizePromptText(title, 240)
	if normalizedPrompt == "" && normalizedTitle == "" {
		return
	}

	cacheKey := summaryCacheKey(normalizedTitle, normalizedPrompt)
	if cachedSummary(cacheKey) != "" || summaryFailedRecently(cacheKey) {
		return
	}

	summaryCacheMu.Lock()
	if _, ok := summaryInFlight[cacheKey]; ok {
		summaryCacheMu.Unlock()
		return
	}
	summaryInFlight[cacheKey] = struct{}{}
	summaryCacheMu.Unlock()

	request := summaryRequest{
		CacheKey: cacheKey,
		Prompt:   normalizedPrompt,
		Title:    normalizedTitle,
	}
	select {
	case summaryQueue <- request:
	default:
		clearSummaryInFlight(cacheKey)
	}
}

func clearSummaryInFlight(cacheKey string) {
	summaryCacheMu.Lock()
	defer summaryCacheMu.Unlock()
	delete(summaryInFlight, cacheKey)
}

func runSummaryWorker() {
	for request := range summaryQueue {
		summary, err := generateAISummary(request.Prompt, request.Title)
		if err != nil {
			recordSummaryFailure(request.CacheKey)
			clearSummaryInFlight(request.CacheKey)
			log.Printf("ai summary failed: %v", err)
			continue
		}

		summary = normalizeAISummary(summary)
		if !isUsefulSummary(summary) {
			recordSummaryFailure(request.CacheKey)
			clearSummaryInFlight(request.CacheKey)
			continue
		}

		storeSummary(request.CacheKey, summary)
		clearSummaryInFlight(request.CacheKey)
		notifySummaryRefresh()
	}
}

func notifySummaryRefresh() {
	select {
	case summaryRefreshCh <- struct{}{}:
	default:
	}
}

func storeSummary(cacheKey, summary string) {
	updatedAt := time.Now().Unix()

	summaryCacheMu.Lock()
	summaryCache[cacheKey] = summaryCacheEntry{
		Summary:   summary,
		UpdatedAt: updatedAt,
	}
	delete(summaryFailures, cacheKey)
	summaryCacheMu.Unlock()

	if err := saveSummaryCache(); err != nil {
		log.Printf("failed to save summary cache: %v", err)
	}
}

func loadSummaryCache() {
	data, err := os.ReadFile(summaryCacheFile)
	if err != nil {
		return
	}

	var loaded map[string]summaryCacheEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("failed to load summary cache: %v", err)
		return
	}

	summaryCacheMu.Lock()
	defer summaryCacheMu.Unlock()
	summaryCache = loaded
}

func saveSummaryCache() error {
	summaryCacheMu.RLock()
	snapshot := make(map[string]summaryCacheEntry, len(summaryCache))
	for key, entry := range summaryCache {
		snapshot[key] = entry
	}
	summaryCacheMu.RUnlock()

	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	tempPath := summaryCacheFile + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, summaryCacheFile)
}

func generateAISummary(prompt, title string) (string, error) {
	tempFile, err := os.CreateTemp(cacheRoot, "summary-*.txt")
	if err != nil {
		return "", err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	defer os.Remove(tempPath)

	var input strings.Builder
	input.WriteString("You write short glanceable labels for AI agent sessions.\n")
	input.WriteString("Return only the label.\n")
	input.WriteString("Rules:\n")
	input.WriteString("- 2 to 5 words when possible\n")
	input.WriteString("- Title Case\n")
	input.WriteString("- No quotes\n")
	input.WriteString("- No trailing punctuation\n")
	input.WriteString("- Capture the main task, not setup or filler text\n")
	input.WriteString("- Prefer concrete nouns or deliverables over generic verbs\n")
	input.WriteString("- If the request is vague, return the shortest faithful label\n\n")
	if strings.TrimSpace(title) != "" {
		input.WriteString("Recovered title:\n")
		input.WriteString(title)
		input.WriteString("\n\n")
	}
	input.WriteString("Initial prompt:\n")
	input.WriteString(prompt)
	input.WriteString("\n")

	ctx, cancel := context.WithTimeout(context.Background(), aiSummaryTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		codexBinary,
		"exec",
		"-C", cacheRoot,
		"--skip-git-repo-check",
		"--ephemeral",
		"-s", "read-only",
		"-o", tempPath,
		input.String(),
	)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codex exec failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	data, err := os.ReadFile(tempPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func normalizeAISummary(text string) string {
	line := strings.TrimSpace(firstNonEmptyLine(text))
	lower := strings.ToLower(line)
	for _, prefix := range []string{"label:", "summary:"} {
		if strings.HasPrefix(lower, prefix) {
			line = strings.TrimSpace(line[len(prefix):])
			lower = strings.ToLower(line)
		}
	}
	line = strings.Trim(line, "\"'`")
	line = strings.Join(strings.Fields(line), " ")
	return truncateRunes(line, 80)
}

func buildPromptSummary(text string, limit int) string {
	cleaned := normalizePromptText(text, 320)
	if cleaned == "" {
		return ""
	}
	if summary := inferPromptSummary(cleaned); summary != "" {
		return truncateRunes(summary, limit)
	}
	return truncateRunes(humanizeSummary(cleaned), limit)
}

func inferPromptSummary(text string) string {
	cleaned := strings.TrimSpace(text)
	lower := strings.ToLower(cleaned)
	cleaned, lower = stripPromptLeadIn(cleaned, lower)

	if clause := extractEmbeddedActionClause(cleaned, lower); clause != "" && clause != cleaned {
		if summary := inferPromptSummary(clause); summary != "" {
			return summary
		}
	}

	if subject := extractHowWorksSubject(cleaned, lower); subject != "" && hasAnyPrefix(lower, "research ", "reasearch ", "investigate ", "explore ", "review ", "understand ", "explain ") {
		return formatSummary("research", subject)
	}

	if object := extractActionObject(cleaned, lower, []string{"add tests for ", "write tests for ", "create tests for ", "test "}, []string{" so ", " and ", " using ", " with ", " in ", " on "}); object != "" {
		return formatSummary("tests", object)
	}

	if object := extractActionObject(cleaned, lower, []string{"document ", "write docs for ", "write documentation for ", "add docs for ", "add documentation for "}, []string{" so ", " and ", " using ", " with ", " in ", " on "}); object != "" {
		return formatSummary("docs", object)
	}

	if object := extractActionObject(cleaned, lower, []string{"fix ", "resolve ", "debug ", "investigate "}, []string{" when ", " where ", " using ", " with ", " in ", " on ", " for ", " and "}); object != "" {
		if hasAnyPrefix(lower, "debug ", "investigate ") {
			return formatSummary("debug", object)
		}
		return formatSummary("fix", object)
	}

	if object := extractActionObject(cleaned, lower, []string{"research ", "reasearch ", "explore ", "review ", "understand ", "explain "}, []string{" and ", " using ", " with ", " in ", " on "}); object != "" {
		objectLower := strings.ToLower(object)
		if strings.Contains(objectLower, "repo") && (strings.Contains(objectLower, "todo") || strings.Contains(objectLower, "readme")) {
			return "Repo Audit"
		}
		return formatSummary("research", object)
	}
	if hasAnyPrefix(lower, "research ", "reasearch ", "explore ", "review ") && (strings.Contains(lower, "todo") || strings.Contains(lower, "readme")) {
		return "Repo Audit"
	}

	if object := extractActionObject(cleaned, lower, []string{"refactor ", "clean up ", "cleanup ", "improve ", "optimize "}, []string{" using ", " with ", " in ", " on ", " for ", " and "}); object != "" {
		return formatSummary("refactor", object)
	}

	if object := extractActionObject(cleaned, lower, []string{"implement ", "build ", "create ", "add ", "write ", "design "}, []string{" kind of ", ": ", " but ", " so ", " using ", " with ", " in ", " on ", " for ", " that ", " which "}); object != "" {
		return formatSummary("build", object)
	}

	return ""
}

func stripPromptLeadIn(text, lower string) (string, string) {
	prefixes := []string{
		"please ",
		"pls ",
		"can you ",
		"could you ",
		"would you ",
		"help me ",
		"i want you to ",
		"i want to ",
		"i want a ",
		"i want an ",
		"i need you to ",
		"i need to ",
		"i need a ",
		"i need an ",
		"let's ",
		"lets ",
		"in this repo, ",
		"in this repo ",
		"in the repo, ",
		"in the repo ",
		"for this repo, ",
		"for this repo ",
		"in this project, ",
		"in this project ",
		"for this project, ",
		"for this project ",
	}

	changed := true
	for changed {
		changed = false
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				text = strings.TrimSpace(text[len(prefix):])
				lower = strings.ToLower(text)
				changed = true
			}
		}
	}

	return text, lower
}

func extractEmbeddedActionClause(text, lower string) string {
	type actionPattern struct {
		needle string
	}

	patterns := []actionPattern{
		{needle: " implement "},
		{needle: " build "},
		{needle: " create "},
		{needle: " add "},
		{needle: " write "},
		{needle: " design "},
		{needle: " fix "},
		{needle: " debug "},
		{needle: " refactor "},
		{needle: " optimize "},
		{needle: " document "},
		{needle: " test "},
	}

	best := -1
	for _, pattern := range patterns {
		index := strings.LastIndex(lower, pattern.needle)
		if index > best {
			best = index + 1
		}
	}
	if best <= 0 || best >= len(text) {
		return ""
	}
	return strings.TrimSpace(text[best:])
}

func extractHowWorksSubject(text, lower string) string {
	start := strings.Index(lower, "how ")
	if start == -1 {
		return ""
	}
	start += len("how ")
	rest := text[start:]
	restLower := lower[start:]

	for _, suffix := range []string{" works", " work", " is implemented", " behaves"} {
		if index := strings.Index(restLower, suffix); index != -1 {
			return trimSummaryObject(rest[:index])
		}
	}
	return ""
}

func hasAnyPrefix(text string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func extractActionObject(text, lower string, prefixes []string, stopPhrases []string) string {
	for _, prefix := range prefixes {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		object := strings.TrimSpace(text[len(prefix):])
		object = cutAtAnyPhrase(object, stopPhrases)
		return trimSummaryObject(object)
	}
	return ""
}

func cutAtAnyPhrase(text string, phrases []string) string {
	lower := strings.ToLower(text)
	best := len(text)
	for _, phrase := range phrases {
		index := strings.Index(lower, phrase)
		if index == -1 {
			continue
		}
		if index < best {
			best = index
		}
	}
	if best < len(text) {
		return strings.TrimSpace(text[:best])
	}
	return strings.TrimSpace(text)
}

func trimSummaryObject(text string) string {
	object := normalizePromptText(text, 160)
	if object == "" {
		return ""
	}

	lower := strings.ToLower(object)
	for _, prefix := range []string{
		"the ", "a ", "an ", "this ", "that ", "my ", "our ", "current ", "existing ",
		"this repo, ", "the repo, ", "this project, ", "the project, ",
		"support for ", "supporting ", "tests for ", "test coverage for ", "coverage for ",
		"implementation of ", "investigation into ", "research on ", "review of ",
		"docs for ", "documentation for ",
	} {
		if strings.HasPrefix(lower, prefix) {
			object = strings.TrimSpace(object[len(prefix):])
			lower = strings.ToLower(object)
		}
	}

	for _, suffix := range []string{" bug", " issue", " problem", " error"} {
		if strings.HasSuffix(lower, suffix) {
			object = strings.TrimSpace(object[:len(object)-len(suffix)])
			lower = strings.ToLower(object)
		}
	}

	if lower == "this repo" || lower == "the repo" || lower == "this project" || lower == "the project" {
		return ""
	}

	return object
}

func formatSummary(kind, object string) string {
	object = trimSummaryObject(object)
	if object == "" {
		return ""
	}

	switch kind {
	case "fix":
		return humanizeSummary(object + " fix")
	case "debug":
		return humanizeSummary(object + " debug")
	case "research":
		return humanizeSummary(object + " research")
	case "refactor":
		return humanizeSummary(object + " refactor")
	case "tests":
		return humanizeSummary(object + " tests")
	case "docs":
		return humanizeSummary(object + " docs")
	default:
		return humanizeSummary(object)
	}
}

func humanizeSummary(text string) string {
	acronyms := map[string]string{
		"ai": "AI", "api": "API", "cli": "CLI", "cpu": "CPU", "css": "CSS", "db": "DB",
		"gpu": "GPU", "html": "HTML", "http": "HTTP", "https": "HTTPS", "id": "ID",
		"io": "IO", "json": "JSON", "jwt": "JWT", "llm": "LLM", "oauth": "OAuth",
		"rpc": "RPC", "sdk": "SDK", "sql": "SQL", "ssh": "SSH", "tcp": "TCP",
		"tls": "TLS", "ts": "TS", "ui": "UI", "url": "URL", "ux": "UX", "yaml": "YAML",
		"websocket": "Websocket", "ws": "WS",
	}
	stopWords := map[string]struct{}{
		"and": {}, "as": {}, "at": {}, "by": {}, "for": {}, "from": {}, "in": {},
		"into": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "with": {},
	}

	words := strings.Fields(strings.TrimSpace(text))
	for index, word := range words {
		lower := strings.ToLower(word)
		if replacement, ok := acronyms[lower]; ok {
			words[index] = replacement
			continue
		}
		if index > 0 {
			if _, ok := stopWords[lower]; ok {
				words[index] = lower
				continue
			}
		}
		if len(word) == 0 {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

func isUsefulSummary(summary string) bool {
	lower := strings.ToLower(strings.TrimSpace(summary))
	switch lower {
	case "", "ai agent", "agent", "claude", "codex", "new chat", "untitled", "chat":
		return false
	default:
		return !strings.HasPrefix(lower, "ai:")
	}
}

func shouldPreferPromptSummary(titleSummary, promptSummary string) bool {
	if !isUsefulSummary(titleSummary) {
		return true
	}

	titleLower := strings.ToLower(strings.TrimSpace(titleSummary))
	if strings.Contains(titleLower, "readme") || strings.Contains(titleLower, "todo") || strings.Contains(titleLower, "repo,") {
		return true
	}

	return len([]rune(promptSummary))+8 < len([]rune(titleSummary))
}

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

func detectAgent(win window, processes map[int]procInfo, children map[int][]int) *detectedAgent {
	if win.PID <= 0 {
		return nil
	}

	for _, descendantPID := range walkDescendants(win.PID, children) {
		proc, ok := processes[descendantPID]
		if !ok {
			continue
		}
		if _, ok := agentNames[proc.ExeName]; !ok {
			continue
		}

		position := [2]int{999, 999}
		if len(win.Layout.PosInScrollingLayout) >= 2 {
			position = [2]int{win.Layout.PosInScrollingLayout[0], win.Layout.PosInScrollingLayout[1]}
		}

		return &detectedAgent{
			WindowID:        win.ID,
			WindowPID:       win.PID,
			WorkspaceID:     win.WorkspaceID,
			Tool:            proc.ExeName,
			ToolDisplay:     strings.Title(proc.ExeName),
			AgentPID:        descendantPID,
			Cwd:             proc.Cwd,
			AgentStartEpoch: processStartEpoch(proc),
			WindowTitle:     win.Title,
			Focused:         win.IsFocused,
			Position:        position,
			FocusOrder:      [2]int64{win.FocusTimestamp.Secs, win.FocusTimestamp.Nanos},
		}
	}

	return nil
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
			if selected.CreatedAt > 0 && agent.AgentStartEpoch > 0 && math.Abs(float64(selected.CreatedAt)-agent.AgentStartEpoch) > 6*3600 {
				continue
			}

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

func finalizeAgent(agent detectedAgent, recovered map[int]recoveredPrompt) finalAgent {
	metadata := parseSessionFile(agent.WindowPID)
	recoveredData, hasRecovered := recovered[agent.AgentPID]

	projectDir := metadata["project_dir"]
	if projectDir == "" && hasRecovered {
		projectDir = recoveredData.ProjectDir
	}
	if projectDir == "" {
		projectDir = agent.Cwd
	}

	label := metadata["label"]
	if label == "" && hasRecovered {
		label = recoveredData.Label
	}
	if label == "" {
		label = titleLabel(agent.WindowTitle, agent.Tool)
	}

	promptFirstLine := metadata["prompt_first_line"]
	if promptFirstLine == "" && hasRecovered {
		promptFirstLine = recoveredData.PromptFirstLine
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
