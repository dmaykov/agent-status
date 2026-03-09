package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

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

	cacheKey := summaryCacheKey(currentSummaryProviderSelection(), normalizedTitle, normalizedPrompt)
	return cachedSummary(cacheKey)
}

func summaryCacheKey(provider, title, prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(provider) + "\n===\n" + strings.TrimSpace(title) + "\n---\n" + strings.TrimSpace(prompt)))
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

	cacheKey := summaryCacheKey(currentSummaryProviderSelection(), normalizedTitle, normalizedPrompt)
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
	selectedProvider := currentSummaryProviderSelection()
	effectiveProvider := effectiveSummaryProvider(selectedProvider)
	if effectiveProvider == "" {
		switch selectedProvider {
		case summaryProviderCodex:
			return "", fmt.Errorf("codex is selected for summaries but is not installed")
		case summaryProviderClaude:
			return "", fmt.Errorf("claude is selected for summaries but is not installed")
		default:
			return "", fmt.Errorf("no supported summary provider is installed")
		}
	}

	input := buildAISummaryPrompt(prompt, title)
	switch effectiveProvider {
	case summaryProviderCodex:
		return generateCodexSummary(input)
	case summaryProviderClaude:
		return generateClaudeSummary(input)
	default:
		return "", fmt.Errorf("unsupported summary provider: %s", effectiveProvider)
	}
}

func buildAISummaryPrompt(prompt, title string) string {
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
	input.WriteString("- If the input contains a ticket tag like [ENG-1234] or [PROJ-42], put it first in the label verbatim\n")
	input.WriteString("- If the request is vague, return the shortest faithful label\n\n")
	if strings.TrimSpace(title) != "" {
		input.WriteString("Recovered title:\n")
		input.WriteString(title)
		input.WriteString("\n\n")
	}
	input.WriteString("Initial prompt:\n")
	input.WriteString(prompt)
	input.WriteString("\n")
	return input.String()
}

func generateCodexSummary(input string) (string, error) {
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
		input,
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

func generateClaudeSummary(input string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), aiSummaryTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		claudeBinary,
		"--print",
		"--output-format", "text",
		"--permission-mode", "dontAsk",
		"--tools", "",
		"--no-session-persistence",
		input,
	)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude --print failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
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
