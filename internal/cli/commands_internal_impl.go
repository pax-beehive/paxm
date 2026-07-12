package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

func (r runner) writeHookResult(result capture.Result, jsonOut, codexNative bool) error {
	if codexNative {
		return writeCodexUserPromptHookOutput(r.stdout, result)
	}
	if jsonOut {
		return writeJSON(r.stdout, result)
	}
	if result.Skipped || result.Recall == nil {
		return nil
	}
	writeRecallContextMarkdown(r.stdout, *result.Recall, "passive")
	return nil
}

type codexUserPromptHookOutput struct {
	HookSpecificOutput codexUserPromptHookSpecificOutput `json:"hookSpecificOutput"`
}

type codexUserPromptHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func writeCodexUserPromptHookOutput(w io.Writer, result capture.Result) error {
	if result.Skipped || result.Recall == nil || len(result.Recall.Hits) == 0 {
		return nil
	}
	var context bytes.Buffer
	writeRecallMarkdown(&context, *result.Recall)
	additionalContext := tools.WrapRecallContext("passive", "Relevant memory recalled by paxm:\n\n"+strings.TrimSpace(context.String()))
	if additionalContext == "" {
		return nil
	}
	return writeJSON(w, codexUserPromptHookOutput{
		HookSpecificOutput: codexUserPromptHookSpecificOutput{
			HookEventName:     "UserPromptSubmit",
			AdditionalContext: additionalContext,
		},
	})
}

type hookBufferRequest struct {
	Action  string          `json:"action,omitempty"`
	EventID string          `json:"event_id,omitempty"`
	Target  string          `json:"target"`
	Event   string          `json:"event"`
	Raw     json.RawMessage `json:"raw"`
}

type hookBufferResponse struct {
	OK             bool                   `json:"ok"`
	Buffered       bool                   `json:"buffered,omitempty"`
	Flushed        int                    `json:"flushed,omitempty"`
	ProviderWrites map[string]int         `json:"provider_writes,omitempty"`
	ProviderRefs   map[string]int         `json:"provider_refs,omitempty"`
	ProviderErrors []memory.ProviderError `json:"provider_errors,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

func (r runner) runInternalHook(args []string) error {
	fs := flag.NewFlagSet("__hook", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	target := fs.String("target", "codex", "hook target")
	eventName := fs.String("event", "", "hook event")
	jsonOut := fs.Bool("json", false, "write JSON recall output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := io.ReadAll(r.stdin)
	if err != nil {
		return err
	}
	event, err := decodeHookEvent(raw, *target, *eventName)
	if err != nil {
		return err
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	var rt *paxruntime.Runtime
	defer func() {
		if rt != nil {
			_ = rt.Close()
		}
	}()
	lazyRecall := capture.RecallFunc(func(ctx context.Context, value capture.Event) (capture.Result, error) {
		if rt == nil {
			_, loaded, loadErr := r.loadRuntime()
			if loadErr != nil {
				return capture.Result{}, loadErr
			}
			rt = loaded
		}
		return rt.Capture.Recall(ctx, value)
	})
	handler := capture.Handler{
		Config:      cfg,
		SourceOwner: os.Getenv("PAXM_INTEGRATION_OWNER"),
		Recall:      lazyRecall,
		MarkInitial: func(value capture.Event) (capture.Event, error) { return r.markInitialUserInputRecall(cfg, value), nil },
		Buffer: func(value capture.Event) error {
			started := time.Now()
			response, bufferErr := r.sendHookToBuffer(value)
			r.recordHookWriteTelemetry(cfg, value, response, time.Since(started), bufferErr)
			return bufferErr
		},
		ObserveRecall: func(value capture.Event, result capture.Result, duration time.Duration, recallErr error) {
			query := value.Query
			var recall tools.RecallResult
			if result.Recall != nil {
				query, recall = result.Recall.Query, *result.Recall
			}
			r.recordRecallTelemetry(cfg, "hook_recall", "hook", result.Target, result.Event, hookRecallProfile(cfg, value), query, recall, result.Skipped, duration, recallErr)
		},
	}
	outcome, err := handler.Handle(context.Background(), event)
	if outcome.BufferError != nil {
		_, _ = fmt.Fprintf(r.stderr, "paxm hook buffer skipped: %s\n", outcome.BufferError)
	}
	if err != nil || outcome.Ignored || outcome.Result == nil {
		return err
	}
	codexNative := *jsonOut && outcome.Event.Target == "codex" && outcome.Event.Event == "user_input"
	return r.writeHookResult(*outcome.Result, *jsonOut, codexNative)
}

func (r runner) runHookControl(args []string) error {
	fs := flag.NewFlagSet("__hook-control", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	shutdown := fs.Bool("shutdown", false, "flush and stop the hook daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return flushExistingHookBuffer(r.configFile(), *shutdown)
}

func (r runner) runHookDaemon(args []string) error {
	fs := flag.NewFlagSet("__hook-daemon", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	socket := fs.String("socket", hookSocketPath(r.configFile()), "daemon socket")
	idleTimeout := fs.Duration("idle-timeout", 30*time.Minute, "daemon idle timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	defer func() { _ = rt.Close() }()
	releaseLock, err := acquireHookDaemonLock(r.configFile())
	if err != nil {
		return err
	}
	defer releaseLock()
	captureRuntime, err := r.openHookCaptureRuntime(cfg, rt)
	if err != nil {
		return err
	}
	defer captureRuntime.Close()
	listener, cleanup, err := openHookListener(*socket)
	if err != nil {
		return err
	}
	defer cleanup()
	return r.serveHookDaemon(listener, captureRuntime, *idleTimeout)
}

func (r runner) openHookCaptureRuntime(cfg config.Config, rt *paxruntime.Runtime) (*capture.Runtime, error) {
	queuePath := hookQueuePath(r.configFile())
	if strings.TrimSpace(cfg.CaptureQueue.Path) != "" {
		queuePath = cfg.CaptureQueue.Path
	}
	return capture.Open(capture.OpenOptions{Config: cfg, QueuePath: queuePath, Policy: rt.Capture, Operator: rt.Operator, Record: func(event telemetry.Event) { r.recordTelemetry(cfg, event) }})
}

func openHookListener(socket string) (net.Listener, func(), error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, nil, err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	}
	return listener, cleanup, nil
}

func (r runner) serveHookDaemon(listener net.Listener, captureRuntime *capture.Runtime, idleTimeout time.Duration) error {

	deadline := time.NewTimer(idleTimeout)
	defer deadline.Stop()
	for {
		type acceptResult struct {
			conn net.Conn
			err  error
		}
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, err := listener.Accept()
			accepted <- acceptResult{conn: conn, err: err}
		}()
		select {
		case <-deadline.C:
			_, _ = captureRuntime.Process(context.Background(), capture.Command{Action: "flush"})
			return nil
		case result := <-accepted:
			if result.err != nil {
				return result.err
			}
			flushed, shutdown, err := handleCaptureQueueConn(context.Background(), captureRuntime, result.conn)
			if err != nil {
				_, _ = fmt.Fprintf(r.stderr, "paxm hook buffer error: %s\n", err)
			}
			if shutdown {
				return nil
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(idleTimeout)
			_ = flushed
		}
	}
}

func handleCaptureQueueConn(ctx context.Context, runtime *capture.Runtime, conn net.Conn) (int, bool, error) {
	defer func() { _ = conn.Close() }()
	var request hookBufferRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, false, err
	}
	command := capture.Command{Action: request.Action, EventID: request.EventID}
	var err error
	if request.Action == "" {
		command.Event, err = decodeHookEvent(request.Raw, request.Target, request.Event)
	}
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, false, err
	}
	receipt, err := runtime.Process(ctx, command)
	response := hookBufferResponse{OK: err == nil, Buffered: receipt.Buffered, Flushed: receipt.Flushed}
	if err != nil {
		response.Error = err.Error()
	}
	_ = writeJSON(conn, response)
	return receipt.Flushed, receipt.Shutdown, err
}

func (r runner) sendHookToBuffer(event capture.Event) (hookBufferResponse, error) {
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	if strings.TrimSpace(event.Metadata["event_id"]) == "" {
		event.Metadata["event_id"] = newHookEventID()
	}
	socket := hookSocketPath(r.configFile())
	response, err := sendHookBufferRequest(socket, event)
	if err != nil {
		if startErr := r.startHookDaemon(socket); startErr != nil {
			return hookBufferResponse{}, startErr
		}
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			response, err = sendHookBufferRequest(socket, event)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return hookBufferResponse{}, err
	}
	if !response.OK && response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (r runner) startHookDaemon(socket string) error {
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	cmd := exec.Command(binaryPath, "--config", r.configFile(), "__hook-daemon", "--socket", socket)
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer func() { _ = devNull.Close() }()
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	detachCommand(cmd)
	return cmd.Start()
}

func sendHookBufferRequest(socket string, event capture.Event) (hookBufferResponse, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return hookBufferResponse{}, err
	}
	defer func() { _ = conn.Close() }()
	raw := event.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	request := hookBufferRequest{
		EventID: event.Metadata["event_id"],
		Target:  event.Target,
		Event:   event.Event,
		Raw:     raw,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return hookBufferResponse{}, err
	}
	var response hookBufferResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return hookBufferResponse{}, err
	}
	return response, nil
}

func newHookEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return "evt_" + hex.EncodeToString(bytes)
	}
	return "evt_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func flushExistingHookBuffer(configPath string, shutdown bool) error {
	socketPath := hookSocketPath(configPath)
	if _, err := os.Stat(socketPath); errors.Is(err, os.ErrNotExist) {
		lockPath := hookDaemonLockPath(configPath)
		if pathDoesNotExist(lockPath) {
			return nil
		}
		deadline := time.Now().Add(time.Second)
		for pathDoesNotExist(socketPath) {
			if pathDoesNotExist(lockPath) {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("hook daemon lock exists but socket did not become ready")
			}
			time.Sleep(25 * time.Millisecond)
		}
	} else if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(35 * time.Second)); err != nil {
		return err
	}
	action := "flush"
	if shutdown {
		action = "shutdown"
	}
	if err := json.NewEncoder(conn).Encode(hookBufferRequest{Action: action}); err != nil {
		return err
	}
	var response hookBufferResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(firstNonEmpty(response.Error, "hook buffer flush failed"))
	}
	if shutdown {
		return waitForHookDaemonStop(configPath, 5*time.Second)
	}
	return nil
}

func waitForHookDaemonStop(configPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		socketGone := pathDoesNotExist(hookSocketPath(configPath))
		lockGone := pathDoesNotExist(hookDaemonLockPath(configPath))
		if socketGone && lockGone {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("hook daemon did not stop before timeout")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func pathDoesNotExist(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func decodeHookEvent(raw []byte, target, eventName string) (capture.Event, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var event capture.Event
	typedRaw := raw
	var rawObject map[string]any
	if json.Unmarshal(raw, &rawObject) == nil {
		delete(rawObject, "messages")
		if encoded, err := json.Marshal(rawObject); err == nil {
			typedRaw = encoded
		}
	}
	if err := json.Unmarshal(typedRaw, &event); err != nil {
		return capture.Event{}, fmt.Errorf("decode hook event JSON: %w", err)
	}
	if event.Target == "" {
		event.Target = target
	}
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = eventName
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if event.Prompt == "" {
		event.Prompt = promptFromRawHook(raw)
	}
	enrichHookEventFromRaw(&event, raw)
	event.Raw = append(json.RawMessage(nil), raw...)
	return event, nil
}

func promptFromRawHook(raw []byte) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"prompt", "user_prompt", "input", "message"} {
		value, ok := object[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func enrichHookEventFromRaw(event *capture.Event, raw []byte) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return
	}
	enrichHookFields(event, object)
	enrichHookMessages(event, object)
}

func enrichHookFields(event *capture.Event, object map[string]any) {
	if event.Workspace == "" {
		for _, key := range []string{"workspace", "cwd", "current_dir"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				event.Workspace = value
				break
			}
		}
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	for _, key := range []string{"session_id", "transcript_path", "cwd", "current_dir", "model", "source"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			event.Metadata[key] = value
		}
	}
	if event.Assistant == "" {
		for _, key := range []string{"last_assistant_message", "assistant", "assistant_message", "response", "output"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				event.Assistant = value
				break
			}
		}
	}
}

func enrichHookMessages(event *capture.Event, object map[string]any) {
	if len(event.Messages) == 0 {
		event.Messages = hookMessagesFromRaw(object["messages"])
	}
	if event.Event == "tool_use" || event.Event == "tool_failure" {
		event.Messages = append(event.Messages, hookMessagesFromToolEvent(object)...)
	}
	if event.Target == "codex" && event.Event == "turn_end" {
		if path := strings.TrimSpace(stringField(object, "transcript_path")); path != "" {
			event.Messages = append(event.Messages, codexTranscriptToolMessages(path)...)
		}
	}
	event.Messages = dedupeHookMessages(event.Messages)
}

func codexTranscriptToolMessages(path string) []capture.Message {
	file, err := os.Open(config.ExpandPath(path))
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var messages []capture.Message
	for scanner.Scan() {
		var record struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		kind := strings.ToLower(stringField(record.Payload, "type"))
		if record.Type == "event_msg" && kind == "task_started" {
			messages = nil
			continue
		}
		if record.Type != "response_item" {
			continue
		}
		switch kind {
		case "function_call", "custom_tool_call":
			name := firstNonEmpty(stringField(record.Payload, "name"), stringField(record.Payload, "namespace"))
			input := hookValueText(firstNonNil(record.Payload["arguments"], record.Payload["input"]))
			if text := strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " ")); text != "" {
				messages = append(messages, capture.Message{Role: "tool_call", Text: text, Source: "codex_transcript"})
			}
		case "function_call_output", "custom_tool_call_output":
			if text := hookValueText(record.Payload["output"]); text != "" {
				messages = append(messages, capture.Message{Role: "tool_result", Text: text, Source: "codex_transcript"})
			}
		}
	}
	return dedupeHookMessages(messages)
}

func dedupeHookMessages(messages []capture.Message) []capture.Message {
	seen := make(map[string]struct{}, len(messages))
	result := make([]capture.Message, 0, len(messages))
	for _, message := range messages {
		key := strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + strings.TrimSpace(firstNonEmpty(message.Text, message.Content))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, message)
	}
	return result
}

func hookMessagesFromToolEvent(object map[string]any) []capture.Message {
	name := strings.TrimSpace(stringField(object, "tool_name"))
	input := hookValueText(object["tool_input"])
	response := hookValueText(firstNonNil(object["tool_response"], object["tool_result"], object["output"]))
	if response == "" {
		if failure := strings.TrimSpace(stringField(object, "error")); failure != "" {
			response = "Error: " + failure
		}
	}
	var messages []capture.Message
	if call := strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " ")); call != "" {
		messages = append(messages, capture.Message{Role: "tool_call", Text: call})
	}
	if response != "" {
		messages = append(messages, capture.Message{Role: "tool_result", Text: response})
	}
	return messages
}

func hookMessagesFromRaw(value any) []capture.Message {
	rawMessages, ok := value.([]any)
	if !ok {
		return nil
	}
	messages := make([]capture.Message, 0, len(rawMessages))
	for _, rawMessage := range rawMessages {
		object, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		if nested, ok := object["message"].(map[string]any); ok {
			messages = append(messages, hookMessagesFromRaw([]any{nested})...)
			continue
		}
		role := stringField(object, "role")
		source := stringField(object, "source")
		kind := strings.ToLower(stringField(object, "type"))
		if role == "" {
			switch kind {
			case "tool_use", "tool_call", "function_call":
				if text := formatHookToolCall(object); text != "" {
					messages = append(messages, capture.Message{Role: "tool_call", Text: text, Source: source})
				}
				continue
			case "tool_result", "tool_response", "function_call_output", "function_result":
				if text := hookValueText(firstNonNil(object["content"], object["output"], object["result"])); text != "" {
					messages = append(messages, capture.Message{Role: "tool_result", Text: text, Source: source})
				}
				continue
			case "thinking", "reasoning", "analysis", "redacted_thinking":
				continue
			}
		}
		if text := strings.TrimSpace(firstNonEmpty(stringField(object, "text"), stringField(object, "content"))); role != "" && text != "" {
			messages = append(messages, capture.Message{Role: role, Text: text, Source: source})
		}
		messages = append(messages, hookContentMessages(role, source, object["content"])...)
		messages = append(messages, hookToolCallMessages(source, object["tool_calls"])...)
	}
	return messages
}

func hookContentMessages(defaultRole, source string, value any) []capture.Message {
	blocks, ok := value.([]any)
	if !ok {
		return nil
	}
	var messages []capture.Message
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(firstNonEmpty(stringField(block, "type"), defaultRole))
		switch kind {
		case "thinking", "reasoning", "analysis", "redacted_thinking":
			continue
		case "tool_use", "tool_call", "function_call":
			if text := formatHookToolCall(block); text != "" {
				messages = append(messages, capture.Message{Role: "tool_call", Text: text, Source: source})
			}
		case "tool_result", "tool_response", "function_call_output", "function_result":
			if text := hookValueText(firstNonNil(block["content"], block["output"], block["result"])); text != "" {
				messages = append(messages, capture.Message{Role: "tool_result", Text: text, Source: source})
			}
		default:
			if text := strings.TrimSpace(firstNonEmpty(stringField(block, "text"), stringField(block, "content"))); text != "" {
				messages = append(messages, capture.Message{Role: defaultRole, Text: text, Source: source})
			}
		}
	}
	return messages
}

func hookToolCallMessages(source string, value any) []capture.Message {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	var messages []capture.Message
	for _, value := range values {
		call, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if function, ok := call["function"].(map[string]any); ok {
			call = function
		}
		if text := formatHookToolCall(call); text != "" {
			messages = append(messages, capture.Message{Role: "tool_call", Text: text, Source: source})
		}
	}
	return messages
}

func formatHookToolCall(call map[string]any) string {
	name := strings.TrimSpace(firstNonEmpty(stringField(call, "name"), stringField(call, "tool")))
	input := hookValueText(firstNonNil(call["input"], call["arguments"], call["args"]))
	return strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " "))
}

func hookValueText(value any) string {
	value = sanitizeHookValue(value)
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		var structured any
		if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Unmarshal([]byte(text), &structured) == nil {
			value = sanitizeHookValue(structured)
		} else {
			return text
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
}

func sanitizeHookValue(value any) any {
	switch typed := value.(type) {
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			if clean := sanitizeHookValue(item); clean != nil {
				result = append(result, clean)
			}
		}
		return result
	case map[string]any:
		kind := strings.ToLower(stringField(typed, "type"))
		if kind == "thinking" || kind == "reasoning" || kind == "analysis" || kind == "redacted_thinking" {
			return nil
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if isReasoningField(key) {
				continue
			}
			if clean := sanitizeHookValue(item); clean != nil {
				result[key] = clean
			}
		}
		return result
	default:
		return value
	}
}

func isReasoningField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "thinking", "thinking_content", "reasoning", "reasoning_content", "analysis", "chain_of_thought", "thought", "thoughts", "redacted_thinking":
		return true
	default:
		return false
	}
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
func nonEmptyStrings(values ...string) []string {
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func stringField(object map[string]any, key string) string {
	if value, ok := object[key].(string); ok {
		return value
	}
	return ""
}

func bytesTrimSpace(bytes []byte) []byte {
	return []byte(strings.TrimSpace(string(bytes)))
}

func hookSocketPath(configPath string) string {
	return filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", "paxm-hook.sock")
}

func (r runner) recordRecallTelemetry(cfg config.Config, kind, source, target, hookEvent, profile, query string, result tools.RecallResult, skipped bool, duration time.Duration, opErr error) {
	event := paxruntime.RecallTelemetryEvent(cfg, paxruntime.RecallTelemetryInput{
		Kind:      kind,
		Source:    source,
		Target:    target,
		HookEvent: hookEvent,
		Profile:   profile,
		Result:    result,
		Skipped:   skipped,
		Duration:  duration,
		Err:       opErr,
	})
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	recorder.PrepareQueryEvent(&event, query)
	r.recordTelemetry(cfg, event)
}

func (r runner) recordRememberTelemetry(cfg config.Config, kind, source, profile string, itemCount int, result tools.RememberResult, duration time.Duration, opErr error) {
	event := paxruntime.RememberTelemetryEvent(cfg, paxruntime.RememberTelemetryInput{
		Kind:      kind,
		Source:    source,
		Profile:   profile,
		ItemCount: itemCount,
		Result:    result,
		Duration:  duration,
		Err:       opErr,
	})
	r.recordTelemetry(cfg, event)
}

func (r runner) recordHookWriteTelemetry(cfg config.Config, event capture.Event, response hookBufferResponse, duration time.Duration, opErr error) {
	telemetryEvent := telemetry.Event{
		Time:                 time.Now().UTC(),
		Kind:                 "hook_write",
		Source:               "hook",
		Command:              "hook",
		Target:               event.Target,
		HookEvent:            event.Event,
		Profile:              hookWriteProfile(cfg, event),
		Success:              opErr == nil,
		Skipped:              opErr != nil || !response.Buffered,
		DurationMS:           duration.Milliseconds(),
		ItemCount:            boolInt(response.Buffered),
		Flushed:              response.Flushed,
		ProviderWrites:       response.ProviderWrites,
		ProviderRefs:         response.ProviderRefs,
		ProviderErrorDetails: telemetry.ProviderErrors(response.ProviderErrors),
		Error:                paxruntime.TelemetryError(opErr),
	}
	r.recordTelemetry(cfg, telemetryEvent)
}

func (r runner) recordTelemetry(cfg config.Config, event telemetry.Event) {
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	if err := recorder.Record(event); err != nil {
		_, _ = fmt.Fprintf(r.stderr, "paxm telemetry skipped: %s\n", err)
	}
}

func hookRecallProfile(cfg config.Config, event capture.Event) string {
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if agent, ok := cfg.Agents[event.Target]; ok {
		if hook, ok := agent.Hooks[event.Event]; ok {
			if event.Metadata != nil && event.Metadata[capture.RecallPhaseMetadataKey] == capture.RecallPhaseInitial && hook.Recall.Initial != nil && hook.Recall.Initial.Enabled {
				return paxruntime.EffectiveRecallProfile(cfg, hook.Recall.Initial.Profile)
			}
			return paxruntime.EffectiveRecallProfile(cfg, hook.Recall.Profile)
		}
	}
	return "default"
}

func hookWriteProfile(cfg config.Config, event capture.Event) string {
	if event.Target == "" {
		event.Target = "codex"
	}
	if agent, ok := cfg.Agents[event.Target]; ok {
		if hook, ok := agent.Hooks[event.Event]; ok {
			return paxruntime.EffectiveWriteProfile(hook.Write.Profile)
		}
	}
	return "default"
}

func (r runner) markInitialUserInputRecall(cfg config.Config, event capture.Event) capture.Event {
	_ = cfg // policy eligibility is owned by capture.Handler
	key := hookSessionStateKey(event)
	if key == "" {
		return event
	}
	first, err := markHookSessionSeen(hookSessionStatePath(r.configFile()), key, time.Now().UTC())
	if err != nil {
		_, _ = fmt.Fprintf(r.stderr, "paxm hook state skipped: %s\n", err)
		return event
	}
	if !first {
		return event
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	event.Metadata[capture.RecallPhaseMetadataKey] = capture.RecallPhaseInitial
	return event
}

// Compatibility helpers keep existing tests and integrations on the capture
// policy while the CLI remains only an adapter.
func hookSourceAllowed(cfg config.Config, event capture.Event) bool {
	return capture.SourceAllowed(cfg, event, os.Getenv("PAXM_INTEGRATION_OWNER"))
}

func hookWriteEnabled(cfg config.Config, event capture.Event) bool {
	return capture.WriteEnabled(cfg, event)
}

func hookInitialRecallEnabled(cfg config.Config, event capture.Event) bool {
	return capture.InitialRecallEnabled(cfg, event)
}

func hookSessionStateKey(event capture.Event) string {
	target := event.Target
	if target == "" {
		target = "codex"
	}
	if value := strings.TrimSpace(event.Metadata["session_id"]); value != "" {
		return target + "/session/" + value
	}
	if value := strings.TrimSpace(event.Metadata["transcript_path"]); value != "" {
		return target + "/transcript/" + value
	}
	if value := strings.TrimSpace(event.Workspace); value != "" {
		return target + "/workspace/" + value
	}
	if value := strings.TrimSpace(event.Metadata["cwd"]); value != "" {
		return target + "/workspace/" + value
	}
	return ""
}

func hookSessionStatePath(configPath string) string {
	return filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", "session_state.json")
}

const (
	hookSessionStateVersion    = 1
	hookSessionStateMaxEntries = 1000
	hookSessionStateTTL        = 7 * 24 * time.Hour
)

type hookSessionState struct {
	Version int                  `json:"version"`
	Seen    map[string]time.Time `json:"seen"`
}

func markHookSessionSeen(path, key string, now time.Time) (bool, error) {
	state, err := loadHookSessionState(path)
	if err != nil {
		return false, err
	}
	if state.Seen == nil {
		state.Seen = make(map[string]time.Time)
	}
	pruneHookSessionState(&state, now)
	_, exists := state.Seen[key]
	state.Seen[key] = now.UTC()
	if err := saveHookSessionState(path, state); err != nil {
		return false, err
	}
	return !exists, nil
}

func loadHookSessionState(path string) (hookSessionState, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hookSessionState{Version: hookSessionStateVersion, Seen: make(map[string]time.Time)}, nil
		}
		return hookSessionState{}, err
	}
	var state hookSessionState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return hookSessionState{Version: hookSessionStateVersion, Seen: make(map[string]time.Time)}, nil
	}
	if state.Version == 0 {
		state.Version = hookSessionStateVersion
	}
	if state.Seen == nil {
		state.Seen = make(map[string]time.Time)
	}
	return state, nil
}

func saveHookSessionState(path string, state hookSessionState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state.Version = hookSessionStateVersion
	bytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pruneHookSessionState(state *hookSessionState, now time.Time) {
	cutoff := now.Add(-hookSessionStateTTL)
	for key, seenAt := range state.Seen {
		if seenAt.Before(cutoff) {
			delete(state.Seen, key)
		}
	}
	if len(state.Seen) <= hookSessionStateMaxEntries {
		return
	}
	type seenEntry struct {
		Key    string
		SeenAt time.Time
	}
	entries := make([]seenEntry, 0, len(state.Seen))
	for key, seenAt := range state.Seen {
		entries = append(entries, seenEntry{Key: key, SeenAt: seenAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].SeenAt.Before(entries[j].SeenAt)
	})
	for len(entries) > hookSessionStateMaxEntries {
		delete(state.Seen, entries[0].Key)
		entries = entries[1:]
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type setupOption struct {
	ID    string
	Label string
}
