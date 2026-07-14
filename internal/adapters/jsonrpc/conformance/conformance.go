package conformance

import (
	"context"
	"fmt"
	"strings"
	"time"

	jsonrpcadapter "github.com/pax-beehive/paxm/internal/adapters/jsonrpc"
	"github.com/pax-beehive/paxm/internal/memory"
)

type Check struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Passed   bool   `json:"passed"`
	Skipped  bool   `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Result struct {
	Protocol     string                      `json:"protocol"`
	Passed       bool                        `json:"passed"`
	Capabilities jsonrpcadapter.Capabilities `json:"capabilities"`
	Checks       []Check                     `json:"checks"`
	DurationMS   int64                       `json:"duration_ms"`
}

type Provider interface {
	memory.Provider
	SearchWire(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error)
	Capabilities(context.Context) (jsonrpcadapter.Capabilities, error)
	Delete(context.Context, memory.MemoryRef) error
	PutBatch(context.Context, []memory.MemoryItem) ([]memory.MemoryRef, error)
}

func Run(ctx context.Context, provider Provider) Result {
	started := time.Now()
	result := Result{Protocol: "paxm-jsonrpc-provider-v1", Passed: true}
	add := func(name string, required bool, err error) {
		check := Check{Name: name, Required: required, Passed: err == nil}
		if err != nil {
			check.Error = err.Error()
			if required {
				result.Passed = false
			}
		}
		result.Checks = append(result.Checks, check)
	}
	add("health", true, provider.Health(ctx))
	caps, err := provider.Capabilities(ctx)
	add("capabilities", true, err)
	result.Capabilities = caps
	if err != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	token := fmt.Sprintf("paxm-conformance-%d", time.Now().UnixNano())
	item := memory.MemoryItem{
		Text: "Remember " + token, Metadata: map[string]string{"paxm_conformance": token},
		Origin: memory.MemoryOrigin{UserID: "conformance-user", AgentID: "conformance-agent", SessionID: "conformance-session", TurnID: "conformance-turn"},
		Scope:  memory.MemoryScope{Type: "personal", ID: "conformance-user"},
	}
	ref, putErr := provider.Put(ctx, item)
	add("put acknowledgement", true, putErr)
	if putErr == nil && strings.TrimSpace(ref.ID) == "" {
		add("stable ref id", true, fmt.Errorf("put returned an empty ref id"))
	} else if putErr == nil {
		add("stable ref id", true, nil)
	}
	if putErr == nil {
		hits, searchErr := provider.Search(ctx, memory.SearchQuery{Text: token, Limit: 10, Metadata: item.Metadata})
		if searchErr == nil {
			found := false
			for _, hit := range hits {
				if hit.ID == ref.ID && strings.Contains(hit.Text, token) && hit.Metadata["paxm_conformance"] == token {
					found = true
					break
				}
			}
			if !found {
				searchErr = fmt.Errorf("search did not faithfully return written id, text, and metadata")
			}
		}
		add("search fidelity", true, searchErr)
		if caps.Attribution {
			rawHits, rawErr := provider.SearchWire(ctx, memory.SearchQuery{Text: token, Limit: 10, Metadata: item.Metadata})
			if rawErr == nil {
				rawErr = attributionFidelityError(rawHits, ref.ID, item)
			}
			add("attribution fidelity", true, rawErr)
		} else {
			result.Checks = append(result.Checks, Check{Name: "attribution fidelity", Skipped: true})
		}
	}
	batchTexts := []string{token + " batch one", token + " batch two"}
	batchItems := []memory.MemoryItem{
		{Text: batchTexts[0], Origin: memory.MemoryOrigin{UserID: "batch-user-1", AgentID: "batch-agent", SessionID: "batch-session", TurnID: "batch-turn-1"}, Scope: memory.MemoryScope{Type: "team", ID: "team-1"}},
		{Text: batchTexts[1], Origin: memory.MemoryOrigin{UserID: "batch-user-2", AgentID: "batch-agent", SessionID: "batch-session", TurnID: "batch-turn-2"}, Scope: memory.MemoryScope{Type: "team", ID: "team-1"}},
	}
	batchRefs, batchErr := provider.PutBatch(ctx, batchItems)
	if batchErr == nil && len(batchRefs) != 2 {
		batchErr = fmt.Errorf("putBatch returned %d refs, want 2", len(batchRefs))
	}
	if batchErr == nil {
		seen := map[string]bool{}
		for index, batchRef := range batchRefs {
			if strings.TrimSpace(batchRef.ID) == "" || seen[batchRef.ID] {
				batchErr = fmt.Errorf("putBatch ref %d has an empty or duplicate id", index)
				break
			}
			seen[batchRef.ID] = true
			hits, searchErr := provider.Search(ctx, memory.SearchQuery{Text: batchTexts[index], Limit: 10})
			if searchErr != nil {
				batchErr = searchErr
				break
			}
			found := false
			for _, hit := range hits {
				if hit.ID == batchRef.ID && strings.Contains(hit.Text, batchTexts[index]) {
					found = true
					break
				}
			}
			if !found {
				batchErr = fmt.Errorf("putBatch ref %s is not faithfully searchable", batchRef.ID)
				break
			}
			if caps.Attribution {
				rawHits, rawErr := provider.SearchWire(ctx, memory.SearchQuery{Text: batchTexts[index], Limit: 10})
				if rawErr == nil {
					rawErr = attributionFidelityError(rawHits, batchRef.ID, batchItems[index])
				}
				if rawErr != nil {
					batchErr = fmt.Errorf("putBatch attribution: %w", rawErr)
					break
				}
			}
		}
	}
	batchCheck := "putBatch fallback"
	if caps.PutBatch {
		batchCheck = "putBatch"
	}
	add(batchCheck, true, batchErr)
	if caps.Delete && putErr == nil {
		deleteErr := provider.Delete(ctx, ref)
		if deleteErr == nil {
			hits, searchErr := provider.Search(ctx, memory.SearchQuery{Text: token, Limit: 10})
			if searchErr != nil {
				deleteErr = searchErr
			} else {
				for _, hit := range hits {
					if hit.ID == ref.ID {
						deleteErr = fmt.Errorf("deleted ref is still searchable")
						break
					}
				}
			}
		}
		if deleteErr == nil {
			for _, batchRef := range batchRefs {
				if err := provider.Delete(ctx, batchRef); err != nil {
					deleteErr = fmt.Errorf("clean batch ref %s: %w", batchRef.ID, err)
					break
				}
			}
		}
		add("delete lifecycle", true, deleteErr)
	} else {
		result.Checks = append(result.Checks, Check{Name: "delete lifecycle", Skipped: true})
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

func attributionFidelityError(hits []memory.MemoryHit, refID string, item memory.MemoryItem) error {
	for _, hit := range hits {
		if hit.ID != refID {
			continue
		}
		if hit.Origin != item.Origin || hit.Scope != item.Scope {
			return fmt.Errorf("search attribution = origin %#v scope %#v, want origin %#v scope %#v", hit.Origin, hit.Scope, item.Origin, item.Scope)
		}
		return nil
	}
	return fmt.Errorf("search did not return written ref %q for attribution check", refID)
}
