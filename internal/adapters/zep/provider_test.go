package zep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	zepgo "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/option"
	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestProviderAdapterContract(t *testing.T) {
	relevance := 0.9
	client := &fakeGraphClient{
		batchResult:  []*zepgo.Episode{{UUID: "episode-write"}},
		searchResult: &zepgo.GraphSearchResults{Episodes: []*zepgo.Episode{{UUID: "episode-hit", Content: "zep adapter contract", Relevance: &relevance}}},
	}
	provider, err := newWithClient("zep", config.ProviderConfig{APIKey: "key", UserID: "user-1", SearchScope: "episodes"}, client)
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, provider, contracttest.Expectation{
		Name: "zep", Item: memory.MemoryItem{Text: "zep adapter contract"}, Query: memory.SearchQuery{Text: "zep adapter contract", Limit: 3},
		RefID: "episode-write", HitID: "episode-hit", HitText: "zep adapter contract",
	})
}

type fakeGraphClient struct {
	addRequest    *zepgo.AddDataRequest
	batchRequest  *zepgo.AddDataBatchRequest
	searchRequest *zepgo.GraphSearchQuery
	addResult     *zepgo.Episode
	batchResult   []*zepgo.Episode
	searchResult  *zepgo.GraphSearchResults
	addErr        error
	batchErr      error
	searchErr     error
	deletedGraph  string
	deleteErr     error
}

func (c *fakeGraphClient) Delete(_ context.Context, graphID string, _ ...option.RequestOption) (*zepgo.SuccessResponse, error) {
	c.deletedGraph = graphID
	return &zepgo.SuccessResponse{}, c.deleteErr
}

func (c *fakeGraphClient) Add(_ context.Context, request *zepgo.AddDataRequest, _ ...option.RequestOption) (*zepgo.Episode, error) {
	c.addRequest = request
	if c.addErr != nil {
		return nil, c.addErr
	}
	return c.addResult, nil
}

func (c *fakeGraphClient) AddBatch(_ context.Context, request *zepgo.AddDataBatchRequest, _ ...option.RequestOption) ([]*zepgo.Episode, error) {
	c.batchRequest = request
	if c.batchErr != nil {
		return nil, c.batchErr
	}
	return c.batchResult, nil
}

func (c *fakeGraphClient) Search(_ context.Context, request *zepgo.GraphSearchQuery, _ ...option.RequestOption) (*zepgo.GraphSearchResults, error) {
	c.searchRequest = request
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	return c.searchResult, nil
}

func TestNewValidatesZepConfig(t *testing.T) {
	t.Parallel()

	client := &fakeGraphClient{}
	for name, tc := range map[string]config.ProviderConfig{
		"missing api key": {UserID: "user"},
		"missing target":  {APIKey: "key"},
		"two targets":     {APIKey: "key", UserID: "user", GraphID: "graph"},
		"bad scope":       {APIKey: "key", UserID: "user", SearchScope: "bad"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := newWithClient("zep", tc, client); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestPutAddsTextEpisode(t *testing.T) {
	t.Parallel()

	client := &fakeGraphClient{batchResult: []*zepgo.Episode{{UUID: "episode-1"}}}
	provider, err := newWithClient("zep", config.ProviderConfig{
		APIKey:            "key",
		UserID:            "user-1",
		SearchScope:       "episodes",
		SourceDescription: "paxm memory",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC)
	ref, err := provider.Put(context.Background(), memory.MemoryItem{
		ID:        "memory-1",
		Text:      "Todd prefers YAML config",
		Source:    "test",
		Metadata:  map[string]string{"project": "paxm"},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "episode-1" || ref.Provider != "zep" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
	request := client.batchRequest
	if request == nil {
		t.Fatal("batch request was not captured")
	}
	if len(request.Episodes) != 1 {
		t.Fatalf("unexpected batch request: %#v", request)
	}
	episode := request.Episodes[0]
	if episode.Data != "Todd prefers YAML config" || episode.Type != zepgo.GraphDataTypeText {
		t.Fatalf("unexpected episode payload: %#v", episode)
	}
	if request.UserID == nil || *request.UserID != "user-1" || request.GraphID != nil {
		t.Fatalf("unexpected target: %#v", request)
	}
	if episode.CreatedAt == nil || *episode.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected created_at: %#v", episode.CreatedAt)
	}
	if episode.SourceDescription == nil || *episode.SourceDescription != "paxm memory" {
		t.Fatalf("unexpected source description: %#v", episode.SourceDescription)
	}
	if episode.Metadata["paxm_id"] != "memory-1" || episode.Metadata["project"] != "paxm" {
		t.Fatalf("metadata was not mapped: %#v", episode.Metadata)
	}
}

func TestCleanupEvalScopeDeletesDedicatedGraph(t *testing.T) {
	client := &fakeGraphClient{}
	provider, err := newWithClient("zep", config.ProviderConfig{APIKey: "key", GraphID: "paxm-eval-run"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.CleanupEvalScope(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.deletedGraph != "paxm-eval-run" {
		t.Fatalf("deleted graph = %q", client.deletedGraph)
	}
}

func TestZepMetadataPrioritizesProvenanceWithinProviderLimit(t *testing.T) {
	metadata := make(map[string]string)
	for index := range 20 {
		metadata[fmt.Sprintf("a%02d", index)] = "optional"
	}
	item := memory.ApplyProvenance(memory.MemoryItem{ID: "memory", Source: "test", Tier: memory.TierLTM, Metadata: metadata}, memory.Provenance{
		UserID: "todd", AgentID: "codex-todd", ScopeType: "team", ScopeID: "pax",
	})
	got := toZepMetadata(item)
	if len(got) != 10 {
		t.Fatalf("metadata field count = %d, want 10: %#v", len(got), got)
	}
	for key, want := range map[string]string{
		memory.MetadataUserID: "todd", memory.MetadataAgentID: "codex-todd",
		memory.MetadataScopeType: "team", memory.MetadataScopeID: "pax",
	} {
		if got[key] != want {
			t.Fatalf("metadata[%s] = %#v, want %q: %#v", key, got[key], want, got)
		}
	}
}

func TestSearchMapsGraphResults(t *testing.T) {
	t.Parallel()

	episodeRelevance := 0.9
	episodeScore := 2.0
	edgeScore := 4.0
	nodeRank := 3
	observationRelevance := 1.5
	observationSummary := "Observation summary"
	threadScore := -1.0
	threadSummary := "Thread summary"
	lastSummarizedAt := "2026-07-09T01:02:07Z"
	contextText := "Zep context block"
	client := &fakeGraphClient{
		searchResult: &zepgo.GraphSearchResults{
			Context: &contextText,
			Episodes: []*zepgo.Episode{
				{
					UUID:      "episode-1",
					Content:   "YAML config is the paxm default",
					CreatedAt: "2026-07-09T01:02:03Z",
					Relevance: &episodeRelevance,
					Score:     &episodeScore,
					Metadata:  map[string]interface{}{"project": "paxm"},
				},
			},
			Edges: []*zepgo.EntityEdge{
				{
					UUID:           "edge-1",
					Fact:           "paxm uses Zep graph search",
					Name:           "USES",
					CreatedAt:      "2026-07-09T01:02:04Z",
					Score:          &edgeScore,
					SourceNodeUUID: "node-a",
					TargetNodeUUID: "node-b",
				},
			},
			Nodes: []*zepgo.EntityNode{
				{
					UUID:          "node-1",
					Name:          "Todd",
					Summary:       "Memory owner",
					CreatedAt:     "2026-07-09T01:02:05Z",
					SelectionRank: &nodeRank,
					Attributes:    map[string]interface{}{"kind": "person"},
					Labels:        []string{"Person", "User"},
				},
			},
			Observations: []*zepgo.DerivedNode{
				{
					UUID:      "observation-1",
					Name:      "Prefers table-driven tests",
					Summary:   &observationSummary,
					CreatedAt: "2026-07-09T01:02:06Z",
					Relevance: &observationRelevance,
					Attributes: map[string]interface{}{
						"source": "tests",
					},
					Labels:     []string{"Observation"},
					EpisodeIDs: []string{"episode-1", "episode-2"},
				},
			},
			ThreadSummaries: []*zepgo.GraphitiSagaNode{
				{
					UUID:             "summary-1",
					Name:             "Coverage work",
					Summary:          &threadSummary,
					CreatedAt:        "2026-07-09T01:02:08Z",
					Score:            &threadScore,
					Labels:           []string{"Thread"},
					LastSummarizedAt: &lastSummarizedAt,
				},
			},
		},
	}
	provider, err := newWithClient("zep", config.ProviderConfig{
		APIKey:      "key",
		GraphID:     "graph-1",
		SearchScope: "episodes",
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:  "paxm config",
		Limit: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := client.searchRequest
	if request == nil {
		t.Fatal("search request was not captured")
	}
	if request.GraphID == nil || *request.GraphID != "graph-1" || request.UserID != nil {
		t.Fatalf("unexpected search target: %#v", request)
	}
	if request.Scope == nil || *request.Scope != zepgo.GraphSearchScopeEpisodes {
		t.Fatalf("unexpected scope: %#v", request.Scope)
	}
	if request.Limit == nil || *request.Limit != 50 {
		t.Fatalf("expected clamped limit 50, got %#v", request.Limit)
	}
	if request.ReturnRawResults == nil || !*request.ReturnRawResults {
		t.Fatalf("expected raw results request: %#v", request.ReturnRawResults)
	}
	if len(hits) != 6 {
		t.Fatalf("expected context, episode, edge, node, observation, and summary hits, got %#v", hits)
	}
	if hits[1].ID != "episode-1" || hits[1].Relevance != 0.9 || hits[1].RawScore == nil || *hits[1].RawScore != 2 {
		t.Fatalf("episode hit was not mapped: %#v", hits[1])
	}
	if hits[2].ID != "edge-1" || hits[2].Relevance != 0.2 || hits[2].Metadata["zep_edge_name"] != "USES" {
		t.Fatalf("edge hit was not normalized: %#v", hits[2])
	}
	if hits[3].ID != "node-1" || hits[3].Text != "Todd\nMemory owner" || hits[3].Relevance != 0.25 || hits[3].Metadata["zep_labels"] != "Person,User" {
		t.Fatalf("node hit was not mapped: %#v", hits[3])
	}
	if hits[4].ID != "observation-1" || hits[4].Relevance != 1 || hits[4].Metadata["zep_episode_uuids"] != "episode-1,episode-2" {
		t.Fatalf("observation hit was not mapped: %#v", hits[4])
	}
	if hits[5].ID != "summary-1" || hits[5].Relevance != 0 || hits[5].Metadata["zep_last_summarized_at"] != lastSummarizedAt {
		t.Fatalf("thread summary hit was not mapped: %#v", hits[5])
	}
}

func TestProviderReturnsClientErrors(t *testing.T) {
	t.Parallel()

	client := &fakeGraphClient{
		batchErr:  errors.New("add failed"),
		searchErr: errors.New("search failed"),
	}
	provider, err := newWithClient("zep", config.ProviderConfig{APIKey: "key", UserID: "user"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: "memory"}); err == nil {
		t.Fatalf("expected put error")
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "memory"}); err == nil {
		t.Fatalf("expected search error")
	}
}

func TestProviderHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("constructor name and health", func(t *testing.T) {
		provider, err := New("zep-live", config.ProviderConfig{
			APIKey:  "key",
			UserID:  "user",
			BaseURL: "https://example.invalid",
		})
		if err != nil {
			t.Fatal(err)
		}
		if provider.Name() != "zep-live" {
			t.Fatalf("Name() = %q", provider.Name())
		}
		if err := provider.Health(context.Background()); err != nil {
			t.Fatalf("Health() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := provider.Health(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Health(canceled) error = %v", err)
		}
	})

	t.Run("batch edge cases", func(t *testing.T) {
		tests := []struct {
			name    string
			client  *fakeGraphClient
			items   []memory.MemoryItem
			wantErr string
			wantNil bool
		}{
			{name: "empty batch", client: &fakeGraphClient{}, wantNil: true},
			{name: "blank text", client: &fakeGraphClient{}, items: []memory.MemoryItem{{Text: " "}}, wantErr: "memory text is required"},
			{name: "no uuids", client: &fakeGraphClient{batchResult: []*zepgo.Episode{{UUID: " "}, nil}}, items: []memory.MemoryItem{{Text: "memory"}}, wantErr: "episode uuids"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				provider, err := newWithClient("zep", config.ProviderConfig{APIKey: "key", GraphID: "graph"}, tt.client)
				if err != nil {
					t.Fatal(err)
				}
				refs, err := provider.PutBatch(context.Background(), tt.items)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("PutBatch() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("PutBatch() error = %v", err)
				}
				if tt.wantNil && refs != nil {
					t.Fatalf("PutBatch() refs = %#v, want nil", refs)
				}
			})
		}
	})
}
