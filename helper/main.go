package main

import (
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	log.SetFlags(0)
	if err := runCommand(os.Args[1:]); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	} else if len(os.Args) > 1 {
		return
	}
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

func runCommand(args []string) error {
	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "set-summary-provider":
		if len(args) != 2 {
			return fmt.Errorf("usage: %s set-summary-provider <%s|%s|%s>", os.Args[0], summaryProviderAuto, summaryProviderCodex, summaryProviderClaude)
		}
		return writeSummaryProviderSetting(args[1])
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
