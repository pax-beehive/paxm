package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	name        string
	searchErr   error
	putErr      error
	healthErr   error
	hits        []MemoryHit
	refs        []MemoryRef
	searchDelay time.Duration
}

func (p fakeProvider) Name() string {
	return p.name
}

func (p fakeProvider) Search(context.Context, SearchQuery) ([]MemoryHit, error) {
	if p.searchDelay > 0 {
		time.Sleep(p.searchDelay)
	}
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
	return p.healthErr
}

type captureBatchProvider struct {
	fakeProvider
	items []MemoryItem
}

type captureSearchProvider struct {
	fakeProvider
	queries []SearchQuery
}

func (p *captureSearchProvider) Search(_ context.Context, query SearchQuery) ([]MemoryHit, error) {
	p.queries = append(p.queries, query)
	return p.hits, p.searchErr
}

func (p *captureBatchProvider) PutBatch(_ context.Context, items []MemoryItem) ([]MemoryRef, error) {
	p.items = append([]MemoryItem(nil), items...)
	return []MemoryRef{
		{Provider: p.name, ID: "batch-1"},
		{Provider: p.name, ID: "batch-2"},
	}, nil
}

type cleanupProvider struct {
	fakeProvider
	deleted int
	err     error
	limits  []int
}

type blockingSearchProvider struct {
	calls   chan struct{}
	release chan struct{}
}

func (p *blockingSearchProvider) Name() string { return "blocked" }
func (p *blockingSearchProvider) Search(context.Context, SearchQuery) ([]MemoryHit, error) {
	p.calls <- struct{}{}
	<-p.release
	return nil, nil
}
func (p *blockingSearchProvider) Put(context.Context, MemoryItem) (MemoryRef, error) {
	return MemoryRef{}, nil
}
func (p *blockingSearchProvider) Health(context.Context) error { return nil }

type blockingWriteProvider struct {
	calls   chan struct{}
	release chan struct{}
}

func (p *blockingWriteProvider) Name() string { return "blocked" }
func (p *blockingWriteProvider) Search(context.Context, SearchQuery) ([]MemoryHit, error) {
	return nil, nil
}
func (p *blockingWriteProvider) Put(context.Context, MemoryItem) (MemoryRef, error) {
	p.calls <- struct{}{}
	<-p.release
	return MemoryRef{ID: "late"}, nil
}
func (p *blockingWriteProvider) Health(context.Context) error { return nil }

type closeProvider struct {
	fakeProvider
	closed bool
	err    error
}

func (p *closeProvider) Close() error {
	p.closed = true
	return p.err
}

func (p *cleanupProvider) CleanupExpired(_ context.Context, limit int) (int, error) {
	p.limits = append(p.limits, limit)
	if p.err != nil {
		return 0, p.err
	}
	return p.deleted, nil
}

func TestRouterSearchFansOutAndDedupes(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "a", hits: []MemoryHit{{ID: "1", Text: "same memory", Relevance: 0.4}}},
			Read:     true,
			Write:    true,
		},
		{
			Provider: fakeProvider{name: "b", hits: []MemoryHit{{ID: "2", Text: "same memory", Relevance: 0.9}}, searchDelay: 20 * time.Millisecond},
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
	if result.Hits[0].Provider != "b" || result.Hits[0].ID != "2" {
		t.Fatalf("dedupe did not keep the highest-scoring hit: %#v", result.Hits[0])
	}
}

func TestRouterKeepsIdenticalTextFromDifferentScopes(t *testing.T) {
	t.Parallel()
	personal := map[string]string{MetadataScopeType: "personal", MetadataScopeID: "todd"}
	team := map[string]string{MetadataScopeType: "team", MetadataScopeID: "pax"}
	router, err := NewRouter([]ProviderBinding{
		{Provider: fakeProvider{name: "a", hits: []MemoryHit{{ID: "1", Text: "same decision", Relevance: 0.8, Metadata: personal}}}, Read: true},
		{Provider: fakeProvider{name: "b", hits: []MemoryHit{{ID: "2", Text: "same decision", Relevance: 0.7, Metadata: team}}}, Read: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Search(context.Background(), SearchQuery{Text: "decision", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("different scopes were deduped: %#v", result.Hits)
	}
}

func TestLongTermFingerprintIncludesScope(t *testing.T) {
	personal := longTermFingerprint("same decision", map[string]string{MetadataScopeType: "personal", MetadataScopeID: "todd"})
	team := longTermFingerprint("same decision", map[string]string{MetadataScopeType: "team", MetadataScopeID: "pax"})
	if personal == team {
		t.Fatal("different scopes produced the same LTM fingerprint")
	}
}

func TestRouterClosesProviderResources(t *testing.T) {
	provider := &closeProvider{fakeProvider: fakeProvider{name: "sqlite"}}
	router, err := NewRouter([]ProviderBinding{{Provider: provider}})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if !provider.closed {
		t.Fatal("provider was not closed")
	}

	failed := &closeProvider{fakeProvider: fakeProvider{name: "failed"}, err: errors.New("close failed")}
	router, err = NewRouter([]ProviderBinding{{Provider: failed}})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err == nil || !strings.Contains(err.Error(), "failed: close failed") {
		t.Fatalf("Close() error = %v", err)
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

func TestRouterReturnsPartialResultsWhenOptionalProviderTimesOut(t *testing.T) {
	router, err := NewRouter([]ProviderBinding{
		{Provider: fakeProvider{name: "fast", hits: []MemoryHit{{ID: "fast", Text: "memory", Relevance: 1}}}, Read: true},
		{Provider: fakeProvider{name: "slow", searchDelay: 250 * time.Millisecond}, Read: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory"}, SearchPolicy{Providers: []ProviderRoute{
		{Name: "fast", Timeout: 20 * time.Millisecond},
		{Name: "slow", Timeout: 20 * time.Millisecond},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("optional provider blocked recall for %s", elapsed)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "fast" {
		t.Fatalf("partial hits = %#v, want fast hit", result.Hits)
	}
	if len(result.ProviderErrors) != 1 || result.ProviderErrors[0].Provider != "slow" {
		t.Fatalf("provider errors = %#v, want slow timeout", result.ProviderErrors)
	}
	if len(result.ProviderRecalls) != 2 {
		t.Fatalf("provider recall timings = %#v, want two samples", result.ProviderRecalls)
	}
	byProvider := map[string]ProviderRecall{}
	for _, recall := range result.ProviderRecalls {
		byProvider[recall.Provider] = recall
	}
	if byProvider["fast"].Outcome != ProviderRecallSuccess || byProvider["slow"].Outcome != ProviderRecallTimeout {
		t.Fatalf("provider recall outcomes = %#v", byProvider)
	}
	if byProvider["slow"].TimeoutMS != 20 || byProvider["slow"].DurationMS < 10 {
		t.Fatalf("slow provider timing = %#v", byProvider["slow"])
	}
}

func TestRouterFailsFastWhenRequiredProviderTimesOut(t *testing.T) {
	router, err := NewRouter([]ProviderBinding{{Provider: fakeProvider{name: "slow", searchDelay: 250 * time.Millisecond}, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory"}, SearchPolicy{Providers: []ProviderRoute{{Name: "slow", Required: true, Timeout: 20 * time.Millisecond}}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("required provider timeout error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("required provider timeout returned after %s", elapsed)
	}
	if len(result.ProviderErrors) != 1 || !result.ProviderErrors[0].Required {
		t.Fatalf("provider errors = %#v", result.ProviderErrors)
	}
}

func TestRouterBulkheadPreventsRepeatedCallsToStuckProvider(t *testing.T) {
	provider := &blockingSearchProvider{calls: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(provider.release)
	router, err := NewRouter([]ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	policy := SearchPolicy{Providers: []ProviderRoute{{Name: "blocked", Timeout: 20 * time.Millisecond}}}
	var second SearchResult
	for i := range 2 {
		result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			second = result
		}
	}
	if got := len(provider.calls); got != 1 {
		t.Fatalf("stuck provider calls = %d, want 1", got)
	}
	if len(second.ProviderRecalls) != 1 || !second.ProviderRecalls[0].BulkheadBusy || second.ProviderRecalls[0].Outcome != ProviderRecallTimeout {
		t.Fatalf("bulkhead timing missing: %#v", second.ProviderRecalls)
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

func TestRouterCalibratesAllProviderScoreDistributions(t *testing.T) {
	t.Parallel()
	providers := []struct {
		name         string
		rawScoreKind string
		scores       []float64
	}{
		{name: "sqlite", rawScoreKind: "sqlite_fts_bm25_negated", scores: []float64{1, 0.75}},
		{name: "zep", rawScoreKind: "zep_relevance", scores: []float64{1, 0.99993503}},
		{name: "mem0", rawScoreKind: "mem0_score", scores: []float64{0.91, 0.82}},
		{name: "mem0_cloud", rawScoreKind: "mem0_cloud_score", scores: []float64{0.2793, 0.21}},
		{name: "memos", rawScoreKind: "memos_relativity", scores: []float64{0.88, 0.67}},
		{name: "memos_cloud", rawScoreKind: "memos_relativity", scores: []float64{0.42, 0.31}},
		{name: "jsonrpc", rawScoreKind: "jsonrpc_relevance", scores: []float64{0.9, 0.6}},
	}
	bindings := make([]ProviderBinding, 0, len(providers))
	for _, provider := range providers {
		hits := []MemoryHit{
			{ID: provider.name + "-top", Text: provider.name + " top", Relevance: provider.scores[0], RawScoreKind: provider.rawScoreKind},
			{ID: provider.name + "-second", Text: provider.name + " second", Relevance: provider.scores[1], RawScoreKind: provider.rawScoreKind},
		}
		bindings = append(bindings, ProviderBinding{Provider: fakeProvider{name: provider.name, hits: hits}, Read: true})
	}
	router, err := NewRouter(bindings)
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory", Limit: 10}, SearchPolicy{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 10 {
		t.Fatalf("hits = %#v", result.Hits)
	}
	seen := make(map[string]bool)
	for _, hit := range result.Hits {
		seen[hit.Provider] = true
	}
	for _, provider := range providers {
		if !seen[provider.name] {
			t.Fatalf("provider %q was dominated after calibration: %#v", provider.name, result.Hits)
		}
	}
}

func TestRouterCalibrationPreventsFlatProviderFromMonopolizingResults(t *testing.T) {
	t.Parallel()

	flat := make([]MemoryHit, 20)
	for i := range flat {
		flat[i] = MemoryHit{ID: fmt.Sprintf("zep-%02d", i), Text: fmt.Sprintf("flat %d", i), Relevance: 0.9999 - float64(i)*0.000001}
	}
	router, err := NewRouter([]ProviderBinding{
		{Provider: fakeProvider{name: "zep", hits: flat}, Read: true},
		{Provider: fakeProvider{name: "sqlite", hits: []MemoryHit{{ID: "sqlite-1", Text: "sqlite top", Relevance: 1}, {ID: "sqlite-2", Text: "sqlite second", Relevance: 0.7}}}, Read: true},
		{Provider: fakeProvider{name: "mem0", hits: []MemoryHit{{ID: "mem0-1", Text: "mem0 top", Relevance: 0.8}, {ID: "mem0-2", Text: "mem0 second", Relevance: 0.5}}}, Read: true},
		{Provider: fakeProvider{name: "mem0-cloud", hits: []MemoryHit{{ID: "cloud-1", Text: "cloud top", Relevance: 0.21}, {ID: "cloud-2", Text: "cloud second", Relevance: 0.2}}}, Read: true},
		{Provider: fakeProvider{name: "jsonrpc", hits: []MemoryHit{{ID: "rpc-1", Text: "rpc top", Relevance: 0.9}, {ID: "rpc-2", Text: "rpc second", Relevance: 0.6}}}, Read: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory", Limit: 10}, SearchPolicy{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	positions := make(map[string]int)
	for index, hit := range result.Hits {
		counts[hit.Provider]++
		positions[hit.ID] = index
	}
	for _, provider := range []string{"sqlite", "zep", "mem0", "mem0-cloud", "jsonrpc"} {
		if counts[provider] == 0 {
			t.Fatalf("provider %q absent from realistic top-10: %#v", provider, result.Hits)
		}
	}
	if counts["zep"] > 3 {
		t.Fatalf("flat provider monopolized top-10 with %d hits: %#v", counts["zep"], result.Hits)
	}
	if positions["cloud-1"] >= positions["zep-02"] {
		t.Fatalf("third flat Zep hit outranked Cloud relevance 0.21: %#v", result.Hits)
	}
}

func TestRouterCalibrationRanksOnlyPolicyEligibleCandidates(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	valid := MemoryHit{ID: "valid", Text: "valid memory", Relevance: 0.8, Tier: TierLTM}
	search := func(hits []MemoryHit) MemoryHit {
		t.Helper()
		router, err := NewRouter([]ProviderBinding{{Provider: fakeProvider{name: "provider", hits: hits}, Read: true}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory", Limit: 5}, SearchPolicy{
			Limit: 5, MinRelevance: 0.5, Tiers: []MemoryTier{TierLTM},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Hits) != 1 || result.Hits[0].ID != valid.ID {
			t.Fatalf("eligible hits = %#v", result.Hits)
		}
		return result.Hits[0]
	}

	solo := search([]MemoryHit{valid})
	withIneligible := search([]MemoryHit{
		{ID: "expired", Text: "expired", Relevance: 1, Tier: TierLTM, ExpiresAt: &expired},
		{ID: "wrong-tier", Text: "short term", Relevance: 0.95, Tier: TierSTM},
		{ID: "below-threshold", Text: "weak", Relevance: 0.4, Tier: TierLTM},
		valid,
	})
	if withIneligible.rankingScore != solo.rankingScore || withIneligible.rankingScore != valid.Relevance {
		t.Fatalf("ineligible candidates changed rank: solo=%#v with=%#v", solo, withIneligible)
	}
}

func TestRouterSearchOversamplesProviderCandidatesBeforeFinalLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limit     int
		wantLimit int
	}{
		{name: "triple small result limit", limit: 3, wantLimit: 9},
		{name: "cap oversampling", limit: 40, wantLimit: 100},
		{name: "never reduce requested limit", limit: 101, wantLimit: 101},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &captureSearchProvider{fakeProvider: fakeProvider{name: "a", hits: []MemoryHit{
				{ID: "1", Text: "one", Relevance: 1},
				{ID: "2", Text: "two", Relevance: 0.9},
				{ID: "3", Text: "three", Relevance: 0.8},
				{ID: "4", Text: "four", Relevance: 0.7},
			}}}
			router, err := NewRouter([]ProviderBinding{{Provider: provider, Read: true}})
			if err != nil {
				t.Fatal(err)
			}

			result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "memory"}, SearchPolicy{Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.queries) != 1 || provider.queries[0].Limit != tt.wantLimit {
				t.Fatalf("provider candidate limit = %#v, want %d", provider.queries, tt.wantLimit)
			}
			if len(result.Hits) != 4 && tt.limit >= 4 {
				t.Fatalf("final result count = %d, want 4", len(result.Hits))
			}
			if len(result.Hits) != 3 && tt.limit == 3 {
				t.Fatalf("final result count = %d, want 3", len(result.Hits))
			}
		})
	}
}

func TestRouterSearchAppliesProviderRouteThresholdOverrides(t *testing.T) {
	t.Parallel()

	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "strict", hits: []MemoryHit{
				{ID: "strict-low", Text: "strict low", Relevance: 0.6},
				{ID: "strict-high", Text: "strict high", Relevance: 0.9},
			}},
			Read: true,
		},
		{
			Provider: fakeProvider{name: "loose", hits: []MemoryHit{
				{ID: "loose-low", Text: "loose low", Relevance: 0.4},
			}},
			Read: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "threshold"}, SearchPolicy{
		Providers: []ProviderRoute{
			{Name: "strict", Required: true, Weight: 1},
			{Name: "loose", Required: true, Weight: 1, MinRelevance: 0.3, MinScore: 0.3},
		},
		MinRelevance: 0.75,
		MinScore:     0.75,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("expected strict high and loose low hits, got %#v", result.Hits)
	}
	ids := map[string]bool{}
	for _, hit := range result.Hits {
		ids[hit.ID] = true
	}
	if !ids["strict-high"] || !ids["loose-low"] || ids["strict-low"] {
		t.Fatalf("provider thresholds were not applied: %#v", result.Hits)
	}
}

func TestRouterSearchFiltersMemoryTiersAndExpiredHits(t *testing.T) {
	t.Parallel()

	expired := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	router, err := NewRouter([]ProviderBinding{
		{
			Provider: fakeProvider{name: "a", hits: []MemoryHit{
				{ID: "stm", Text: "active working note", Relevance: 1, Tier: TierSTM, ExpiresAt: &future},
				{ID: "ltm", Text: "durable note", Relevance: 1, Metadata: map[string]string{"paxm_tier": "ltm"}},
				{ID: "expired", Text: "old working note", Relevance: 1, Tier: TierSTM, ExpiresAt: &expired},
			}},
			Read: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "note"}, SearchPolicy{Tiers: []MemoryTier{TierSTM}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "stm" {
		t.Fatalf("unexpected tier-filtered hits: %#v", result.Hits)
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

func TestRouterPutPolicyAppliesTierAndExpiry(t *testing.T) {
	t.Parallel()

	provider := &captureBatchProvider{fakeProvider: fakeProvider{name: "writer"}}
	router, err := NewRouter([]ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	_, err = router.PutBatchWithPolicy(context.Background(), []MemoryItem{{
		Text:      "working state",
		CreatedAt: createdAt,
	}}, PutPolicy{Tier: TierSTM, ExpiresAfter: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 1 || provider.items[0].Tier != TierSTM {
		t.Fatalf("tier was not applied: %#v", provider.items)
	}
	if provider.items[0].ExpiresAt == nil || !provider.items[0].ExpiresAt.Equal(createdAt.Add(24*time.Hour)) {
		t.Fatalf("expiry was not applied: %#v", provider.items[0].ExpiresAt)
	}
}

func TestRouterCleanupExpiredUsesCapableProviders(t *testing.T) {
	t.Parallel()

	sqliteProvider := &cleanupProvider{fakeProvider: fakeProvider{name: "sqlite"}, deleted: 2}
	optionalProvider := &cleanupProvider{fakeProvider: fakeProvider{name: "optional"}, err: errors.New("cleanup down")}
	router, err := NewRouter([]ProviderBinding{
		{Provider: fakeProvider{name: "plain"}},
		{Provider: sqliteProvider, Required: true},
		{Provider: optionalProvider},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.CleanupExpired(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 {
		t.Fatalf("CleanupExpired deleted %d rows, want 2", result.Deleted)
	}
	if len(result.ProviderErrors) != 1 || result.ProviderErrors[0].Provider != "optional" || result.ProviderErrors[0].Op != "cleanup_expired" {
		t.Fatalf("unexpected provider errors: %#v", result.ProviderErrors)
	}
	if len(sqliteProvider.limits) != 1 || sqliteProvider.limits[0] != 12 {
		t.Fatalf("sqlite cleanup limits = %#v, want [12]", sqliteProvider.limits)
	}
	if len(optionalProvider.limits) != 1 || optionalProvider.limits[0] != 12 {
		t.Fatalf("optional cleanup limits = %#v, want [12]", optionalProvider.limits)
	}
}

func TestRouterCleanupExpiredFailsOnRequiredProviderError(t *testing.T) {
	t.Parallel()

	requiredProvider := &cleanupProvider{fakeProvider: fakeProvider{name: "sqlite"}, err: errors.New("locked")}
	router, err := NewRouter([]ProviderBinding{{Provider: requiredProvider, Required: true}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := router.CleanupExpired(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "sqlite: locked") {
		t.Fatalf("CleanupExpired error = %v, want required provider error", err)
	}
	if len(result.ProviderErrors) != 1 || !result.ProviderErrors[0].Required {
		t.Fatalf("unexpected provider errors: %#v", result.ProviderErrors)
	}
	if len(requiredProvider.limits) != 1 || requiredProvider.limits[0] != 500 {
		t.Fatalf("default cleanup limit = %#v, want [500]", requiredProvider.limits)
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

func TestRouterPutTimeoutDoesNotWaitForStuckOptionalProvider(t *testing.T) {
	provider := &blockingWriteProvider{calls: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(provider.release)
	router, err := NewRouter([]ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	policy := PutPolicy{Providers: []ProviderRoute{{Name: "blocked", Timeout: 20 * time.Millisecond}}}

	started := time.Now()
	result, err := router.PutWithPolicy(context.Background(), MemoryItem{Text: "memory"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("optional provider blocked write for %s", elapsed)
	}
	if len(result.ProviderErrors) != 1 || !strings.Contains(result.ProviderErrors[0].Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("provider errors = %#v, want timeout", result.ProviderErrors)
	}

	if _, err := router.PutWithPolicy(context.Background(), MemoryItem{Text: "memory again"}, policy); err != nil {
		t.Fatal(err)
	}
	if got := len(provider.calls); got != 1 {
		t.Fatalf("stuck provider calls = %d, want 1", got)
	}
}

func TestRouterPutTimeoutFailsRequiredProvider(t *testing.T) {
	provider := &blockingWriteProvider{calls: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(provider.release)
	router, err := NewRouter([]ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.PutWithPolicy(context.Background(), MemoryItem{Text: "memory"}, PutPolicy{Providers: []ProviderRoute{{Name: "blocked", Required: true, Timeout: 20 * time.Millisecond}}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("required provider timeout error = %v", err)
	}
	if len(result.ProviderErrors) != 1 || !result.ProviderErrors[0].Required {
		t.Fatalf("provider errors = %#v", result.ProviderErrors)
	}
}

func TestRouterValidationHealthAndErrorTables(t *testing.T) {
	t.Parallel()

	t.Run("constructor validation", func(t *testing.T) {
		tests := []struct {
			name     string
			bindings []ProviderBinding
			wantErr  string
		}{
			{name: "nil provider", bindings: []ProviderBinding{{}}, wantErr: "provider is nil"},
			{name: "empty name", bindings: []ProviderBinding{{Provider: fakeProvider{name: ""}}}, wantErr: "provider name is empty"},
			{name: "duplicate name", bindings: []ProviderBinding{{Provider: fakeProvider{name: "a"}}, {Provider: fakeProvider{name: "a"}}}, wantErr: "duplicated"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := NewRouter(tt.bindings); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewRouter() error = %v, want %q", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("health", func(t *testing.T) {
		tests := []struct {
			name      string
			bindings  []ProviderBinding
			wantErr   string
			wantCount int
			wantOK    map[string]bool
		}{
			{name: "no providers", wantErr: "no memory providers are enabled"},
			{
				name: "optional health error is reported without failing",
				bindings: []ProviderBinding{
					{Provider: fakeProvider{name: "required"}, Required: true},
					{Provider: fakeProvider{name: "optional", healthErr: errors.New("offline")}},
				},
				wantCount: 2,
				wantOK:    map[string]bool{"required": true, "optional": false},
			},
			{
				name: "required health error fails",
				bindings: []ProviderBinding{
					{Provider: fakeProvider{name: "required", healthErr: errors.New("down")}, Required: true},
				},
				wantErr:   "required: down",
				wantCount: 1,
				wantOK:    map[string]bool{"required": false},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				router, err := NewRouter(tt.bindings)
				if err != nil {
					t.Fatal(err)
				}
				statuses, err := router.Health(context.Background())
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("Health() error = %v, want %q", err, tt.wantErr)
					}
				} else if err != nil {
					t.Fatalf("Health() error = %v", err)
				}
				if len(statuses) != tt.wantCount {
					t.Fatalf("statuses = %#v, want %d", statuses, tt.wantCount)
				}
				for _, status := range statuses {
					if want, ok := tt.wantOK[status.Provider]; ok && status.OK != want {
						t.Fatalf("status for %s = %#v, want ok=%v", status.Provider, status, want)
					}
				}
			})
		}
	})

	t.Run("search and put errors", func(t *testing.T) {
		router, err := NewRouter([]ProviderBinding{
			{Provider: fakeProvider{name: "reader", searchErr: errors.New("search down")}, Read: true, Required: true},
			{Provider: fakeProvider{name: "writer", putErr: errors.New("write down")}, Write: true, Required: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name string
			run  func() error
			want string
		}{
			{name: "missing search route", run: func() error {
				_, err := router.SearchWithPolicy(context.Background(), SearchQuery{Text: "q"}, SearchPolicy{Providers: []ProviderRoute{{Name: "missing"}}})
				return err
			}, want: `provider "missing" in search policy is not enabled`},
			{name: "required search provider error", run: func() error {
				_, err := router.Search(context.Background(), SearchQuery{Text: "q"})
				return err
			}, want: "reader: search down"},
			{name: "missing put route", run: func() error {
				_, err := router.PutWithPolicy(context.Background(), MemoryItem{Text: "m"}, PutPolicy{Providers: []ProviderRoute{{Name: "missing"}}})
				return err
			}, want: `provider "missing" in put policy is not enabled`},
			{name: "required put provider error", run: func() error {
				_, err := router.Put(context.Background(), MemoryItem{Text: "m"})
				return err
			}, want: "writer: write down"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if err := tt.run(); err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("%s error = %v, want %q", tt.name, err, tt.want)
				}
			})
		}
	})

	t.Run("score helpers", func(t *testing.T) {
		if got := normalizedRelevance(MemoryHit{Relevance: -1}); got != 0 {
			t.Fatalf("negative relevance = %v", got)
		}
		if got := normalizedRelevance(MemoryHit{Relevance: 2}); got != 1 {
			t.Fatalf("clamped relevance = %v", got)
		}
		if got := normalizedRelevance(MemoryHit{Score: 0.4}); got != 0.4 {
			t.Fatalf("score fallback relevance = %v", got)
		}
		if got := recencyScore(time.Now().Add(time.Hour), 0.5); got != 0.5 {
			t.Fatalf("future recency score = %v", got)
		}
		if got := recencyScore(time.Time{}, 0.5); got != 0 {
			t.Fatalf("zero recency score = %v", got)
		}
		if got := dedupeKey(MemoryHit{Provider: "p", ID: "id"}); got != "id:p:id" {
			t.Fatalf("dedupeKey(id) = %q", got)
		}
	})
}
