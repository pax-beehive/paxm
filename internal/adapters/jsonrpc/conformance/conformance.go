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
	item := memory.MemoryItem{Text: "Remember " + token, Metadata: map[string]string{"paxm_conformance": token}}
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
	}
	batchTexts := []string{token + " batch one", token + " batch two"}
	batchRefs, batchErr := provider.PutBatch(ctx, []memory.MemoryItem{{Text: batchTexts[0]}, {Text: batchTexts[1]}})
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
