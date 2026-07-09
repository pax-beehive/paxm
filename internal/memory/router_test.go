package memory

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	name      string
	searchErr error
	putErr    error
	hits      []MemoryHit
	refs      []MemoryRef
}

func (p fakeProvider) Name() string {
	return p.name
}

func (p fakeProvider) Search(context.Context, SearchQuery) ([]MemoryHit, error) {
	if p.searchErr != nil {
		return nil, p.searchErr
	}
	return p.hits, nil
}

func (p fakeProvider) Put(context.Context, MemoryItem) (MemoryRef, error) {
	if p.putErr != nil {
		return MemoryRef{}, p.putErr
	}
	if len(p.refs) > 0 {
		return p.refs[0], nil
	}
	return MemoryRef{Provider: p.name, ID: "ref"}, nil
}

func (p fakeProvider) Health(context.Context) error {
	return nil
}

type captureBatchProvider struct {
	fakeProvider
	items []MemoryItem
}

func (p *captureBatchProvider) PutBatch(_ context.Context, items []MemoryItem) ([]MemoryRef, error) {
	p.items = append([]MemoryItem(nil), items...)
	return []MemoryRef{
		{Provider: p.name, ID: "batch-1"},
		{Provider: p.name, ID: "batch-2"},
	}, nil
}

func TestRouterSearchFansOutAndDedupes(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "a", hits: []MemoryHit{{ID: "1", Text: "same memory", Score: 0.5}}},
			Read:     true,
			Write:    true,
			Weight:   2,
		},
		{
			Provider: fakeProvider{name: "b", hits: []MemoryHit{{ID: "2", Text: "same memory", Score: 0.9}}},
			Read:     true,
			Write:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.Search(context.Background(), SearchQuery{Text: "memory", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("expected deduped hit, got %d", len(result.Hits))
	}
	if result.Hits[0].Provider == "" {
		t.Fatalf("provider was not assigned: %#v", result.Hits[0])
	}
}

func TestRouterIgnoresOptionalProviderErrors(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "required", hits: []MemoryHit{{ID: "1", Text: "memory", Score: 1}}},
			Read:     true,
			Required: true,
		},
		{
			Provider: fakeProvider{name: "optional", searchErr: errors.New("offline")},
			Read:     true,
			Required: false,
			Write:    false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.Search(context.Background(), SearchQuery{Text: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("expected required provider hit, got %d", len(result.Hits))
	}
	if len(result.ProviderErrors) != 1 || result.ProviderErrors[0].Provider != "optional" {
		t.Fatalf("expected optional provider error, got %#v", result.ProviderErrors)
	}
}

func TestRouterSearchAppliesPolicyThresholds(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "a", hits: []MemoryHit{
				{ID: "low", Text: "low relevance", Relevance: 0.2},
				{ID: "high", Text: "high relevance", Relevance: 0.8},
			}},
			Read: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "relevance"}, SearchPolicy{
		Providers:    []ProviderRoute{{Name: "a", Required: true, Weight: 0.5}},
		MinRelevance: 0.25,
		MinScore:     0.35,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "high" {
		t.Fatalf("unexpected filtered hits: %#v", result.Hits)
	}
	if result.Hits[0].Score != 0.4 {
		t.Fatalf("expected weighted final score, got %f", result.Hits[0].Score)
	}
}

func TestRouterPutWritesToAllWritableProviders(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{Provider: fakeProvider{name: "read-only"}, Read: true},
		{Provider: fakeProvider{name: "writer-a"}, Write: true},
		{Provider: fakeProvider{name: "writer-b"}, Write: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.Put(context.Background(), MemoryItem{Text: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refs) != 2 {
		t.Fatalf("expected two refs, got %d", len(result.Refs))
	}
}

func TestRouterPutBatchUsesProviderBatchAPI(t *testing.T) {
	t.Parallel()

	provider := &captureBatchProvider{fakeProvider: fakeProvider{name: "writer"}}
	router, err := NewRouter([]ProviderBinding{
		{Provider: provider, Write: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.PutBatchWithPolicy(context.Background(), []MemoryItem{
		{Text: "one"},
		{Text: "two"},
	}, PutPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 2 {
		t.Fatalf("batch provider did not receive all items: %#v", provider.items)
	}
	if len(result.Refs) != 2 || result.Refs[0].Provider != "writer" || result.Refs[1].Provider != "writer" {
		t.Fatalf("unexpected batch refs: %#v", result.Refs)
	}
}
