package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestServerServesMemoryToolsOverStdio(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Identity.UserID = "todd"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"dev"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"paxm_remember","arguments":{"text":"paxm mcp mode remembers provider fan-out","metadata":{"topic":"mcp"}}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"paxm_recall","arguments":{"query":"mcp provider fan-out","limit":3}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"paxm_history","arguments":{"days":7}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"paxm_config_doctor","arguments":{}}}`,
	}, "\n") + "\n"

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Serve(Options{
		ConfigPath: configPath,
		AgentName:  "codex",
		Version:    "test",
		Stdin:      strings.NewReader(input),
		Stdout:     &stdout,
		Stderr:     &stderr,
	}); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}

	responses := decodeResponses(t, stdout.String())
	if len(responses) != 6 {
		t.Fatalf("expected 4 responses, got %d: %s", len(responses), stdout.String())
	}
	assertNoRPCError(t, responses)

	var initResult map[string]any
	decodeResult(t, responses[0], &initResult)
	if initResult["protocolVersion"] != protocolVersion {
		t.Fatalf("unexpected initialize result: %#v", initResult)
	}

	var listResult struct {
		Tools []toolDefinition `json:"tools"`
	}
	decodeResult(t, responses[1], &listResult)
	if got := toolNames(listResult.Tools); strings.Join(got, ",") != "paxm_recall,paxm_remember,paxm_history,paxm_config_doctor" {
		t.Fatalf("unexpected tools: %#v", got)
	}

	var rememberResult toolResult
	decodeResult(t, responses[2], &rememberResult)
	if rememberResult.IsError || !strings.Contains(rememberResult.Content[0].Text, `"refs"`) {
		t.Fatalf("unexpected remember result: %#v", rememberResult)
	}

	var recallResult toolResult
	decodeResult(t, responses[3], &recallResult)
	if recallResult.IsError || !strings.Contains(recallResult.Content[0].Text, "paxm mcp mode remembers") {
		t.Fatalf("unexpected recall result: %#v", recallResult)
	}
	for _, marker := range []string{`<paxm-recall version="1" mode="active">`, `</paxm-recall>`} {
		if !strings.Contains(recallResult.Content[0].Text, marker) {
			t.Fatalf("recall result omitted envelope %q: %#v", marker, recallResult)
		}
	}
	for _, provenance := range []string{`"scope_type": "personal"`, `"scope_id": "todd"`, `"user_id": "todd"`, `"agent_id": "codex-todd"`} {
		if !strings.Contains(recallResult.Content[0].Text, provenance) {
			t.Fatalf("recall result omitted %q: %#v", provenance, recallResult)
		}
	}
	structured, ok := recallResult.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("recall structured content has unexpected type: %#v", recallResult.StructuredContent)
	}
	context, ok := structured["paxm_context"].(map[string]any)
	if !ok || context["kind"] != "recall" || context["mode"] != "active" {
		t.Fatalf("recall structured content omitted provenance: %#v", structured)
	}

	var historyResult toolResult
	decodeResult(t, responses[4], &historyResult)
	if historyResult.IsError || !strings.Contains(historyResult.Content[0].Text, `"recalls"`) || !strings.Contains(historyResult.Content[0].Text, `"writes"`) {
		t.Fatalf("unexpected history result: %#v", historyResult)
	}

	var doctorResult toolResult
	decodeResult(t, responses[5], &doctorResult)
	if doctorResult.IsError || !strings.Contains(doctorResult.Content[0].Text, `"provider": "sqlite"`) {
		t.Fatalf("unexpected doctor result: %#v", doctorResult)
	}

}

func TestRecallErrorToolResultMarksPartialHits(t *testing.T) {
	t.Parallel()
	result := recallErrorToolResult(errors.New("required provider failed"), facade.RecallResult{
		Query: "deployment",
		Hits:  []memory.MemoryHit{{Provider: "sqlite", ID: "partial", Text: "partial recalled memory"}},
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, `<paxm-recall version="1" mode="active">`) || !strings.Contains(result.Content[0].Text, "partial recalled memory") {
		t.Fatalf("partial recall error omitted text provenance: %#v", result)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("partial recall structured content has unexpected type: %#v", result.StructuredContent)
	}
	context, ok := structured["paxm_context"].(map[string]any)
	if !ok || context["kind"] != "recall" || context["mode"] != "active" {
		t.Fatalf("partial recall error omitted structured provenance: %#v", structured)
	}
}

func TestServerParseErrorRespondsWithNullID(t *testing.T) {
	var stdout bytes.Buffer
	if err := Serve(Options{
		Stdin:  strings.NewReader("{not-json}\n"),
		Stdout: &stdout,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"id":null`) || !strings.Contains(stdout.String(), `"code":-32700`) {
		t.Fatalf("unexpected parse error response: %s", stdout.String())
	}
}

func TestServerRejectsInvalidToolArguments(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"paxm_recall","arguments":{"query":"x","extra":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"paxm_recall","arguments":{"query":"x","limit":0}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"paxm_recall","arguments":{"query":"   "}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"paxm_history","arguments":{"days":0}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"paxm_remember","arguments":{"text":""}}}`,
	}, "\n") + "\n"

	var stdout bytes.Buffer
	if err := Serve(Options{
		ConfigPath: configPath,
		Stdin:      strings.NewReader(input),
		Stdout:     &stdout,
	}); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, stdout.String())
	assertNoRPCError(t, responses)
	if len(responses) != 5 {
		t.Fatalf("expected 4 responses, got %d: %s", len(responses), stdout.String())
	}
	for _, response := range responses {
		var result toolResult
		decodeResult(t, response, &result)
		if !result.IsError {
			t.Fatalf("expected tool error for id %s: %#v", response.ID, result)
		}
	}
	output := stdout.String()
	for _, expected := range []string{
		"unknown field",
		"limit must be at least 1",
		"query is required",
		"days must be at least 1",
		"text is required",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in invalid argument output: %s", expected, output)
		}
	}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func decodeResponses(t *testing.T, output string) []rpcResponse {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]rpcResponse, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func assertNoRPCError(t *testing.T, responses []rpcResponse) {
	t.Helper()
	for _, response := range responses {
		if response.Error != nil {
			t.Fatalf("unexpected rpc error for id %s: %#v", response.ID, response.Error)
		}
	}
}

func decodeResult(t *testing.T, response rpcResponse, out any) {
	t.Helper()
	if err := json.Unmarshal(response.Result, out); err != nil {
		t.Fatalf("decode result for id %s: %v\n%s", response.ID, err, string(response.Result))
	}
}

func toolNames(tools []toolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
