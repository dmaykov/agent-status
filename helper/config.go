package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	inferredPromptMaxLead    = 2 * time.Minute
	inferredPromptMaxLag     = 20 * time.Minute
	freshAgentPlaceholderAge = 2 * time.Minute
	freshInferredMaxLead     = 20 * time.Second
	freshInferredMaxLag      = 5 * time.Minute
	summaryProviderAuto      = "auto"
	summaryProviderCodex     = "codex"
	summaryProviderClaude    = "claude"
)

var (
	homeDir          = getenv("HOME", mustHomeDir())
	cacheRoot        = filepath.Join(getenv("XDG_CACHE_HOME", filepath.Join(homeDir, ".cache")), "agent-status")
	stateFile        = filepath.Join(cacheRoot, "state.json")
	settingsFile     = filepath.Join(cacheRoot, "settings.json")
	sessionsDir      = filepath.Join(cacheRoot, "sessions")
	summaryCacheFile = filepath.Join(cacheRoot, "summary-cache.json")
	codexStateDB     = filepath.Join(homeDir, ".codex", "state_5.sqlite")
	claudeProjects   = filepath.Join(homeDir, ".claude", "projects")
	agentNames       = map[string]struct{}{"codex": {}, "claude": {}}
	bootTime         = readBootTime()
	clockTicks       = readClockTicks()
	sqlite3Binary    = findBinary("sqlite3")
	codexBinary      = findBinary("codex")
	claudeBinary     = findBinary("claude")
	claudeSessionMap = map[string]claudeSessionCache{}
	summaryCache     = map[string]summaryCacheEntry{}
	summaryFailures  = map[string]time.Time{}
	summaryInFlight  = map[string]struct{}{}
	summaryQueue     = make(chan summaryRequest, 64)
	summaryRefreshCh = make(chan struct{}, 1)
	summaryCacheMu   sync.RWMutex
)

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

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func ensureDirs() error {
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(sessionsDir, 0o755)
}
