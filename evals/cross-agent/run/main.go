package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pax-beehive/paxm/internal/crossagent"
)

func main() {
	root := flag.String("root", "", "artifact directory; defaults to a new /tmp directory")
	scenarios := flag.String("scenarios", filepath.Join("evals", "cross-agent", "scenarios"), "scenario directory")
	pi := flag.String("pi", "/opt/homebrew/bin/pi", "Pi executable")
	claude := flag.String("claude", "/Users/toddzheng/.local/bin/claude", "Claude Code executable")
	budget := flag.String("claude-budget", "0.50", "maximum USD per Claude trial")
	only := flag.String("only", "", "run only one scenario id")
	flag.Parse()

	if *root == "" {
		var err error
		*root, err = os.MkdirTemp("", "paxm-cross-agent-report-")
		if err != nil {
			panic(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	report, err := crossagent.Run(context.Background(), crossagent.Options{
		Root: *root, ScenarioDir: *scenarios, PiBinary: *pi, ClaudeBinary: *claude,
		Timeout: 5 * time.Minute, ClaudeBudget: *budget, OnlyScenario: *only,
		DeniedPaths: []string{
			cwd,
			filepath.Join(home, ".codex"),
			filepath.Join(home, ".pi", "agent"),
			filepath.Join(home, ".claude", "projects"),
			filepath.Join(home, ".claude", "history.jsonl"),
			filepath.Join(home, ".config", "paxm"),
			filepath.Join(home, ".local", "share", "paxm"),
		},
	})
	encoded, encodeErr := json.MarshalIndent(report, "", "  ")
	if encodeErr == nil {
		fmt.Println(string(encoded))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
