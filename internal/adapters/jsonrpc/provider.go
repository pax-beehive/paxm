package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const (
	defaultTransport   = "stdio"
	defaultTimeout     = 30 * time.Second
	methodHealth       = "paxm.health"
	methodSearch       = "paxm.search"
	methodPut          = "paxm.put"
	methodPutBatch     = "paxm.putBatch"
	methodDelete       = "paxm.delete"
	methodCapabilities = "paxm.capabilities"
	methodNotFound     = -32601
	maxStderrBytes     = 4096
)

type Provider struct {
	name      string
	transport string
	command   string
	args      []string
	env       map[string]string
	timeout   time.Duration
	nextID    atomic.Int64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("json-rpc error %d", e.Code)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type searchResult struct {
	Hits []memory.MemoryHit `json:"hits"`
}

type putBatchParams struct {
	Items []memory.MemoryItem `json:"items"`
}

type refsResult struct {
	Ref  *memory.MemoryRef  `json:"ref,omitempty"`
	Refs []memory.MemoryRef `json:"refs,omitempty"`
}

type Capabilities struct {
	PutBatch bool `json:"put_batch"`
	Delete   bool `json:"delete"`
}

func New(name string, cfg config.ProviderConfig) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("jsonrpc provider name is required")
	}
	transport := strings.TrimSpace(firstNonEmpty(cfg.Transport, defaultTransport))
	if transport != defaultTransport {
		return nil, fmt.Errorf("jsonrpc provider transport %q is unsupported", transport)
	}
	command := strings.TrimSpace(config.ExpandPath(cfg.Command))
	if command == "" {
		return nil, errors.New("jsonrpc provider command is required")
	}
	timeout, err := parseTimeout(cfg.Timeout)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(cfg.Env))
	for key, value := range cfg.Env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env[key] = value
	}
	return &Provider{
		name:      name,
		transport: transport,
		command:   command,
		args:      append([]string(nil), cfg.Args...),
		env:       env,
		timeout:   timeout,
	}, nil
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result searchResult
	if err := p.call(ctx, methodSearch, query, &result); err != nil {
		return nil, err
	}
	for i := range result.Hits {
		result.Hits[i].Provider = p.name
	}
	return result.Hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return memory.MemoryRef{}, err
	}
	var result refsResult
	if err := p.call(ctx, methodPut, item, &result); err != nil {
		return memory.MemoryRef{}, err
	}
	refs := normalizeRefs(p.name, result)
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("jsonrpc put did not return a memory ref")
	}
	return refs[0], nil
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	var result refsResult
	err := p.call(ctx, methodPutBatch, putBatchParams{Items: items}, &result)
	if isMethodNotFound(err) {
		return p.putBatchIndividually(ctx, items)
	}
	if err != nil {
		return nil, err
	}
	refs := normalizeRefs(p.name, result)
	if len(refs) == 0 {
		return nil, errors.New("jsonrpc putBatch did not return memory refs")
	}
	return refs, nil
}

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.call(ctx, methodHealth, map[string]any{}, nil)
}

func (p *Provider) Capabilities(ctx context.Context) (Capabilities, error) {
	var result Capabilities
	err := p.call(ctx, methodCapabilities, map[string]any{}, &result)
	if isMethodNotFound(err) {
		return Capabilities{}, nil
	}
	return result, err
}

func (p *Provider) Delete(ctx context.Context, ref memory.MemoryRef) error {
	if strings.TrimSpace(ref.ID) == "" {
		return errors.New("jsonrpc delete requires a memory ref id")
	}
	return p.call(ctx, methodDelete, ref, nil)
}

func (p *Provider) putBatchIndividually(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	refs := make([]memory.MemoryRef, 0, len(items))
	for _, item := range items {
		ref, err := p.Put(ctx, item)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (p *Provider) call(ctx context.Context, method string, params any, result any) error {
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request := rpcRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", p.nextID.Add(1)),
		Method:  method,
		Params:  params,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, p.command, p.args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := newBoundedBuffer(maxStderrBytes)
	cmd.Stderr = stderr
	cmd.Env = p.commandEnv()

	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
	}()

	if _, err := stdin.Write(append(payload, '\n')); err != nil {
		_ = stdin.Close()
		p.kill(cmd)
		<-wait
		return err
	}
	if err := stdin.Close(); err != nil {
		p.kill(cmd)
		<-wait
		return err
	}

	var response rpcResponse
	decodeErr := json.NewDecoder(stdout).Decode(&response)
	p.kill(cmd)
	<-wait
	if ctx.Err() != nil {
		return fmt.Errorf("jsonrpc provider %q %s timed out after %s: %w", p.name, method, timeout, ctx.Err())
	}
	if decodeErr != nil {
		return fmt.Errorf("decode jsonrpc response for %s: %w%s", method, decodeErr, stderrSuffix(stderr.String()))
	}
	if response.ID != request.ID {
		return fmt.Errorf("jsonrpc response id mismatch for %s: got %q, want %q", method, response.ID, request.ID)
	}
	if response.Error != nil {
		return response.Error
	}
	if result != nil {
		if len(response.Result) == 0 || bytes.Equal(bytes.TrimSpace(response.Result), []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode jsonrpc result for %s: %w", method, err)
		}
	}
	return nil
}

func (p *Provider) commandEnv() []string {
	env := os.Environ()
	keys := make([]string, 0, len(p.env))
	for key := range p.env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+p.env[key])
	}
	return env
}

func (p *Provider) kill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func normalizeRefs(provider string, result refsResult) []memory.MemoryRef {
	refs := append([]memory.MemoryRef(nil), result.Refs...)
	if result.Ref != nil {
		refs = append([]memory.MemoryRef{*result.Ref}, refs...)
	}
	for i := range refs {
		refs[i].Provider = provider
	}
	return refs
}

func isMethodNotFound(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == methodNotFound
}

func parseTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultTimeout, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("jsonrpc provider timeout: %w", err)
	}
	if timeout <= 0 {
		return 0, errors.New("jsonrpc provider timeout must be positive")
	}
	return timeout, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stderrSuffix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return ": stderr: " + value
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.buf...))
}

var _ io.Writer = (*boundedBuffer)(nil)
