package zep

import (
	"context"
	"errors"
	"testing"
	"time"

	zepgo "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/option"
	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

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

func TestSearchMapsGraphResults(t *testing.T) {
	t.Parallel()

	episodeRelevance := 0.9
	episodeScore := 2.0
	edgeScore := 4.0
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
	if len(hits) != 3 {
		t.Fatalf("expected context, episode, and edge hits, got %#v", hits)
	}
	if hits[1].ID != "episode-1" || hits[1].Relevance != 0.9 || hits[1].RawScore == nil || *hits[1].RawScore != 2 {
		t.Fatalf("episode hit was not mapped: %#v", hits[1])
	}
	if hits[2].ID != "edge-1" || hits[2].Relevance != 0.2 || hits[2].Metadata["zep_edge_name"] != "USES" {
		t.Fatalf("edge hit was not normalized: %#v", hits[2])
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
