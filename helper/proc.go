package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
