package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type memoryItem struct {
	ID        string            `json:"id,omitempty"`
	Text      string            `json:"text"`
	Source    string            `json:"source,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Tier      string            `json:"tier,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}
type memoryRef struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id"`
}
type searchQuery struct {
	Text     string            `json:"text"`
	Limit    int               `json:"limit,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Tiers    []string          `json:"tiers,omitempty"`
}
type memoryHit struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Relevance float64           `json:"relevance"`
	Score     float64           `json:"score"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Tier      string            `json:"tier,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type store struct {
	Items map[string]memoryItem `json:"items"`
}

func main() {
	var req request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fail("decode request: " + err.Error())
	}
	result, rpcErr := dispatch(req)
	if err := json.NewEncoder(os.Stdout).Encode(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}); err != nil {
		fail(err.Error())
	}
}

func dispatch(req request) (any, *rpcError) {
	switch req.Method {
	case "paxm.health":
		return map[string]bool{"ok": true}, nil
	case "paxm.capabilities":
		return map[string]bool{"put_batch": true, "delete": true}, nil
	case "paxm.put":
		var item memoryItem
		if err := json.Unmarshal(req.Params, &item); err != nil {
			return nil, invalid(err)
		}
		ref, err := put(item)
		if err != nil {
			return nil, internal(err)
		}
		return map[string]any{"ref": ref}, nil
	case "paxm.putBatch":
		var value struct {
			Items []memoryItem `json:"items"`
		}
		if err := json.Unmarshal(req.Params, &value); err != nil {
			return nil, invalid(err)
		}
		refs := make([]memoryRef, 0, len(value.Items))
		for _, item := range value.Items {
			ref, err := put(item)
			if err != nil {
				return nil, internal(err)
			}
			refs = append(refs, ref)
		}
		return map[string]any{"refs": refs}, nil
	case "paxm.search":
		var query searchQuery
		if err := json.Unmarshal(req.Params, &query); err != nil {
			return nil, invalid(err)
		}
		hits, err := search(query)
		if err != nil {
			return nil, internal(err)
		}
		return map[string]any{"hits": hits}, nil
	case "paxm.delete":
		var ref memoryRef
		if err := json.Unmarshal(req.Params, &ref); err != nil {
			return nil, invalid(err)
		}
		if err := remove(ref.ID); err != nil {
			return nil, internal(err)
		}
		return map[string]bool{"deleted": true}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func storePath() string {
	if value := strings.TrimSpace(os.Getenv("PAXM_SAMPLE_PROVIDER_STORE")); value != "" {
		return value
	}
	return "paxm-sample-provider.json"
}
func load() (store, error) {
	value := store{Items: map[string]memoryItem{}}
	data, err := os.ReadFile(storePath())
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	if value.Items == nil {
		value.Items = map[string]memoryItem{}
	}
	return value, err
}
func save(value store) error {
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func put(item memoryItem) (memoryRef, error) {
	value, err := load()
	if err != nil {
		return memoryRef{}, err
	}
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = fmt.Sprintf("sample-%d", time.Now().UnixNano())
	}
	item.ID = id
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	value.Items[id] = item
	return memoryRef{ID: id}, save(value)
}
func search(query searchQuery) ([]memoryHit, error) {
	value, err := load()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query.Text))
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	hits := []memoryHit{}
	for id, item := range value.Items {
		if needle != "" && !strings.Contains(strings.ToLower(item.Text), needle) {
			continue
		}
		matches := true
		for key, want := range query.Metadata {
			if item.Metadata[key] != want {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		hits = append(hits, memoryHit{ID: id, Text: item.Text, Relevance: 1, Score: 1, Metadata: item.Metadata, CreatedAt: item.CreatedAt, Tier: item.Tier, ExpiresAt: item.ExpiresAt})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
func remove(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("ref id is required")
	}
	value, err := load()
	if err != nil {
		return err
	}
	delete(value.Items, id)
	return save(value)
}
func invalid(err error) *rpcError  { return &rpcError{Code: -32602, Message: err.Error()} }
func internal(err error) *rpcError { return &rpcError{Code: -32603, Message: err.Error()} }
func fail(message string)          { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
