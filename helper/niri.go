package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

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
