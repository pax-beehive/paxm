package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/mcp"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/tools"
)

func (r runner) executeHook(event capture.Event, jsonOut, codexNative bool) error {
	cfg, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := rt.Capture.Recall(context.Background(), event)
	query := event.Query
	var recall tools.RecallResult
	if result.Recall != nil {
		query, recall = result.Recall.Query, *result.Recall
	}
	r.recordRecallTelemetry(cfg, "hook_recall", "hook", result.Target, result.Event, hookRecallProfile(cfg, event), query, recall, result.Skipped, time.Since(started), err)
	if err != nil {
		return err
	}
	return r.writeHookResult(result, jsonOut, codexNative)
}

func (r runner) runToolCommand(command string, args []string) error {
	switch command {
	case "recall":
		return r.runRecall(args)
	case "remember":
		return r.runRemember(args)
	case "mcp":
		return r.runMCP(args)
	default:
		return fmt.Errorf("unknown tool command %q", command)
	}
}

func (r runner) runRecall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	query := fs.String("query", "", "recall query")
	queryShort := fs.String("q", "", "recall query")
	profile := fs.String("profile", "", "recall profile")
	limit := fs.Int("limit", 0, "maximum memories to return")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read query from stdin")
	hookEvent := fs.Bool("hook-event", false, "read a hook event from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hookEvent {
		data, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		event, err := decodeHookEvent(data, "codex", "user_input")
		if err != nil {
			return err
		}
		return r.executeHook(event, *jsonOut, false)
	}
	q := firstNonEmpty(*query, *queryShort)
	if *stdin {
		data, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		q = string(data)
	}
	cfg, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := rt.Tools.Recall(context.Background(), tools.RecallInput{Query: q, Profile: *profile, Limit: *limit})
	r.recordRecallTelemetry(cfg, "recall", "cli", "", "", paxruntime.EffectiveRecallProfile(cfg, *profile), firstNonEmpty(result.Query, q), result, false, time.Since(started), err)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeRecallJSON(r.stdout, result, "active")
	}
	writeRecallContextMarkdown(r.stdout, result, "active")
	return nil
}

func (r runner) runRemember(args []string) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	text := fs.String("text", "", "memory text")
	profile := fs.String("profile", "", "write profile")
	source := fs.String("source", "cli", "memory source")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read memory text from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value := *text
	if *stdin {
		data, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		value = string(data)
	}
	cfg, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := rt.Tools.Remember(context.Background(), tools.RememberInput{Text: value, Profile: *profile, Source: *source})
	r.recordRememberTelemetry(cfg, "remember", "cli", paxruntime.EffectiveWriteProfile(*profile), 1, result, time.Since(started), err)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, result)
	}
	for _, ref := range result.Refs {
		fmt.Fprintf(r.stdout, "stored memory: %s/%s\n", ref.Provider, ref.ID)
	}
	return nil
}

func (r runner) runMCP(args []string) error {
	if len(args) == 0 {
		return errors.New("mcp command requires a subcommand: serve")
	}
	if args[0] != "serve" {
		return fmt.Errorf("unknown mcp subcommand %q", args[0])
	}
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return mcp.Serve(mcp.Options{ConfigPath: r.configFile(), Version: r.versionString(), Stdin: r.stdin, Stdout: r.stdout, Stderr: r.stderr})
}

type recallJSONOutput struct {
	tools.RecallResult
	PaxmContext recallJSONContext `json:"paxm_context"`
}
type recallJSONContext struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Mode    string `json:"mode"`
}

func writeRecallJSON(w io.Writer, result tools.RecallResult, mode string) error {
	return writeJSON(w, recallJSONOutput{RecallResult: result, PaxmContext: recallJSONContext{Version: 1, Kind: "recall", Mode: mode}})
}
