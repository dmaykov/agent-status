package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func normalizeSummaryProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", summaryProviderAuto:
		return summaryProviderAuto
	case summaryProviderCodex:
		return summaryProviderCodex
	case summaryProviderClaude:
		return summaryProviderClaude
	default:
		return ""
	}
}

func readHelperSettings() helperSettings {
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return helperSettings{SummaryProvider: summaryProviderAuto}
	}

	var settings helperSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return helperSettings{SummaryProvider: summaryProviderAuto}
	}

	settings.SummaryProvider = normalizeSummaryProvider(settings.SummaryProvider)
	if settings.SummaryProvider == "" {
		settings.SummaryProvider = summaryProviderAuto
	}
	return settings
}

func currentSummaryProviderSelection() string {
	return readHelperSettings().SummaryProvider
}

func currentSummaryProviderState() summaryProvider {
	selected := currentSummaryProviderSelection()
	return summaryProvider{
		Selected:        selected,
		Effective:       effectiveSummaryProvider(selected),
		CodexAvailable:  hasBinary(summaryProviderCodex),
		ClaudeAvailable: hasBinary(summaryProviderClaude),
	}
}

func effectiveSummaryProvider(selected string) string {
	switch normalizeSummaryProvider(selected) {
	case summaryProviderCodex:
		if hasBinary(summaryProviderCodex) {
			return summaryProviderCodex
		}
	case summaryProviderClaude:
		if hasBinary(summaryProviderClaude) {
			return summaryProviderClaude
		}
	default:
		if hasBinary(summaryProviderCodex) {
			return summaryProviderCodex
		}
		if hasBinary(summaryProviderClaude) {
			return summaryProviderClaude
		}
	}
	return ""
}

func writeSummaryProviderSetting(provider string) error {
	normalized := normalizeSummaryProvider(provider)
	if normalized == "" {
		return fmt.Errorf("invalid summary provider: %s", provider)
	}
	if err := ensureDirs(); err != nil {
		return err
	}

	settings := readHelperSettings()
	settings.SummaryProvider = normalized

	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	tempPath := settingsFile + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, settingsFile)
}
