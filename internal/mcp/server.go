package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

const protocolVersion = "2025-11-25"

type Options struct {
	ConfigPath string
	AgentName  string
	Version    string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

type Server struct {
	configPath string
	agentName  string
	version    string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
}

func Serve(opts Options) error {
	server := NewServer(opts)
	return server.Serve(context.Background())
}

func NewServer(opts Options) *Server {
	stdin := opts.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}
	return &Server{
		configPath: opts.ConfigPath,
		agentName:  strings.TrimSpace(opts.AgentName),
		version:    version,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(s.stdout)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		response, ok := s.handleMessage(ctx, line)
		if !ok {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMessage(ctx context.Context, line []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return errorResponse(nil, -32700, "parse error"), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return errorResponse(req.ID, -32600, "invalid request"), len(req.ID) > 0
	}
	if len(req.ID) == 0 {
		if req.Method == "notifications/initialized" || strings.HasPrefix(req.Method, "notifications/") {
			return response{}, false
		}
		return response{}, false
	}
	result, err := s.handleRequest(ctx, req.Method, req.Params)
	if err != nil {
		var rpcErr rpcError
		if errors.As(err, &rpcErr) {
			return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr}, true
		}
		return errorResponse(req.ID, -32603, err.Error()), true
	}
	return response{JSONRPC: "2.0", ID: req.ID, Result: result}, true
}

func (s *Server) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.initializeResult(), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		return s.callTool(ctx, params)
	default:
		return nil, rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]any{
			"name":    "paxm",
			"title":   "paxm memory",
			"version": s.version,
		},
	}
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "paxm_recall",
			Title:       "Recall Memory",
			Description: "Search configured paxm memory providers using the active recall policy.",
			InputSchema: objectSchema(map[string]any{
				"query": map[string]any{"type": "string", "description": "Focused memory search query."},
				"profile": map[string]any{
					"type":        "string",
					"description": "Optional recall profile. Defaults to the configured active recall profile.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Optional maximum number of memories to return.",
				},
				"meta": stringMapSchema("Optional metadata filters or request metadata."),
			}, []string{"query"}),
		},
		{
			Name:        "paxm_remember",
			Title:       "Remember Memory",
			Description: "Store memory through the configured paxm write profile. Use profile=stm for short-term working memory and profile=ltm for durable facts.",
			InputSchema: objectSchema(map[string]any{
				"text":     map[string]any{"type": "string", "description": "Memory text to store."},
				"id":       map[string]any{"type": "string", "description": "Optional caller-stable memory id."},
				"profile":  map[string]any{"type": "string", "description": "Optional write profile. Defaults to the configured default profile."},
				"source":   map[string]any{"type": "string", "description": "Optional source label. Defaults to mcp."},
				"metadata": stringMapSchema("Optional metadata to store with the memory."),
			}, []string{"text"}),
		},
		{
			Name:        "paxm_history",
			Title:       "Memory History",
			Description: "Summarize recent local paxm telemetry.",
			InputSchema: objectSchema(map[string]any{
				"days": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Number of days to summarize. Defaults to 7.",
				},
			}, nil),
		},
		{
			Name:        "paxm_config_doctor",
			Title:       "Config Doctor",
			Description: "Check health for enabled paxm memory providers without returning secrets.",
			InputSchema: objectSchema(nil, nil),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringMapSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolResult struct {
	Content           []textContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (toolResult, error) {
	var call toolCallParams
	if err := decodeParams(params, &call); err != nil {
		return toolResult{}, rpcError{Code: -32602, Message: err.Error()}
	}
	switch call.Name {
	case "paxm_recall":
		return s.callRecall(ctx, call.Arguments), nil
	case "paxm_remember":
		return s.callRemember(ctx, call.Arguments), nil
	case "paxm_history":
		return s.callHistory(call.Arguments), nil
	case "paxm_config_doctor":
		return s.callConfigDoctor(ctx, call.Arguments), nil
	default:
		return toolResult{}, rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", call.Name)}
	}
}

type recallArgs struct {
	Query   string            `json:"query"`
	Profile string            `json:"profile,omitempty"`
	Limit   *int              `json:"limit,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

func (s *Server) callRecall(ctx context.Context, raw json.RawMessage) toolResult {
	var args recallArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return errorToolResult(err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return errorToolResult(errors.New("query is required"))
	}
	limit := 0
	if args.Limit != nil {
		if *args.Limit < 1 {
			return errorToolResult(errors.New("limit must be at least 1"))
		}
		limit = *args.Limit
	}
	rt, err := paxruntime.Load(s.configPath)
	if err != nil {
		return errorToolResult(err)
	}
	started := time.Now()
	result, opErr := rt.Tools.Recall(ctx, tools.RecallInput{
		Query:   args.Query,
		Profile: args.Profile,
		Limit:   limit,
		Meta:    args.Meta,
	})
	if err := s.recordRecall(rt, args.Profile, firstNonEmpty(result.Query, args.Query), result, time.Since(started), opErr); err != nil {
		_, _ = fmt.Fprintf(s.stderr, "paxm telemetry skipped: %s\n", err)
	}
	if opErr != nil {
		return recallErrorToolResult(opErr, result)
	}
	return recallToolResult(result)
}

type rememberArgs struct {
	ID       string            `json:"id,omitempty"`
	Text     string            `json:"text"`
	Profile  string            `json:"profile,omitempty"`
	Source   string            `json:"source,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (s *Server) callRemember(ctx context.Context, raw json.RawMessage) toolResult {
	var args rememberArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return errorToolResult(err)
	}
	if strings.TrimSpace(args.Text) == "" {
		return errorToolResult(errors.New("text is required"))
	}
	source := strings.TrimSpace(args.Source)
	if source == "" {
		source = "mcp"
	}
	rt, err := paxruntime.Load(s.configPath)
	if err != nil {
		return errorToolResult(err)
	}
	started := time.Now()
	result, opErr := rt.Tools.Remember(ctx, tools.RememberInput{
		ID:        args.ID,
		Text:      args.Text,
		Profile:   args.Profile,
		Source:    source,
		Metadata:  args.Metadata,
		AgentName: s.agentName,
	})
	if err := s.recordRemember(rt, args.Profile, 1, result, time.Since(started), opErr); err != nil {
		_, _ = fmt.Fprintf(s.stderr, "paxm telemetry skipped: %s\n", err)
	}
	if opErr != nil {
		return errorToolResultWithContent(opErr, result)
	}
	return structuredToolResult(result)
}

type historyArgs struct {
	Days *int `json:"days,omitempty"`
}

func (s *Server) callHistory(raw json.RawMessage) toolResult {
	var args historyArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return errorToolResult(err)
	}
	cfg, err := config.Load(paxruntime.ConfigFile(s.configPath))
	if err != nil {
		return errorToolResult(err)
	}
	days := args.Days
	if days == nil {
		defaultDays := 7
		days = &defaultDays
	}
	if *days < 1 {
		return errorToolResult(errors.New("days must be at least 1"))
	}
	recorder := telemetry.NewRecorder(cfg.Telemetry, paxruntime.ConfigFile(s.configPath))
	summary, err := recorder.History(*days)
	if err != nil {
		return errorToolResult(err)
	}
	return structuredToolResult(summary)
}

func (s *Server) callConfigDoctor(ctx context.Context, raw json.RawMessage) toolResult {
	var args struct{}
	if err := decodeToolArgs(raw, &args); err != nil {
		return errorToolResult(err)
	}
	rt, err := paxruntime.Load(s.configPath)
	if err != nil {
		return errorToolResult(err)
	}
	statuses, opErr := rt.Health(ctx)
	if opErr != nil {
		return errorToolResultWithContent(opErr, map[string]any{"statuses": statuses})
	}
	return structuredToolResult(statuses)
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	return json.Unmarshal(raw, out)
}

func decodeToolArgs(raw json.RawMessage, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("tool arguments must be a single JSON object")
	}
	return nil
}

func structuredToolResult(value any) toolResult {
	return toolResult{
		Content:           []textContent{{Type: "text", Text: jsonText(value)}},
		StructuredContent: value,
	}
}

func recallToolResult(value tools.RecallResult) toolResult {
	structured := struct {
		tools.RecallResult
		PaxmContext map[string]any `json:"paxm_context"`
	}{
		RecallResult: value,
		PaxmContext:  recallContextMetadata(),
	}
	return toolResult{
		Content:           []textContent{{Type: "text", Text: tools.WrapRecallContext("active", jsonText(value))}},
		StructuredContent: structured,
	}
}

func recallErrorToolResult(err error, result tools.RecallResult) toolResult {
	content := map[string]any{
		"error":        err.Error(),
		"result":       result,
		"paxm_context": recallContextMetadata(),
	}
	return toolResult{
		Content:           []textContent{{Type: "text", Text: tools.WrapRecallContext("active", jsonText(content))}},
		StructuredContent: content,
		IsError:           true,
	}
}

func recallContextMetadata() map[string]any {
	return map[string]any{"version": 1, "kind": "recall", "mode": "active"}
}

func errorToolResult(err error) toolResult {
	return errorToolResultWithContent(err, nil)
}

func errorToolResultWithContent(err error, value any) toolResult {
	content := map[string]any{"error": err.Error()}
	if value != nil {
		content["result"] = value
	}
	return toolResult{
		Content:           []textContent{{Type: "text", Text: jsonText(content)}},
		StructuredContent: content,
		IsError:           true,
	}
}

func jsonText(value any) string {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(bytes)
}

func errorResponse(id json.RawMessage, code int, message string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}
}

func (e rpcError) Error() string {
	return e.Message
}

func (s *Server) recordRecall(rt *paxruntime.Runtime, profile, query string, result tools.RecallResult, duration time.Duration, opErr error) error {
	event := paxruntime.RecallTelemetryEvent(rt.Config, paxruntime.RecallTelemetryInput{
		Kind:     "recall",
		Source:   "mcp",
		Profile:  profile,
		Result:   result,
		Duration: duration,
		Err:      opErr,
	})
	recorder := telemetry.NewRecorder(rt.Config.Telemetry, rt.ConfigPath)
	recorder.PrepareQueryEvent(&event, query)
	return recorder.Record(event)
}

func (s *Server) recordRemember(rt *paxruntime.Runtime, profile string, itemCount int, result tools.RememberResult, duration time.Duration, opErr error) error {
	event := paxruntime.RememberTelemetryEvent(rt.Config, paxruntime.RememberTelemetryInput{
		Kind:      "remember",
		Source:    "mcp",
		Profile:   profile,
		ItemCount: itemCount,
		Result:    result,
		Duration:  duration,
		Err:       opErr,
	})
	recorder := telemetry.NewRecorder(rt.Config.Telemetry, rt.ConfigPath)
	return recorder.Record(event)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
