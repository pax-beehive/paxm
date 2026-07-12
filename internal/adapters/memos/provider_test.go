package memos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) Do(request *http.Request) (*http.Response, error) { return fn(request) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestSelfHostedContract(t *testing.T) {
	client := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/health":
			return jsonResponse(`{"status":"ok"}`), nil
		case "/product/add":
			return jsonResponse(`{"data":[{"id":"write-1"}]}`), nil
		case "/product/search":
			return jsonResponse(`{"data":{"text_mem":[{"memories":[{"id":"hit-1","memory":"contract memory","metadata":{"relativity":0.8}}]}]}}`), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})
	provider, err := newProvider("memos-test", config.ProviderConfig{BaseURL: "http://memos.test", UserID: "u", MemCubeID: "c"}, selfHosted, client, func() string { return "generated" })
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, provider, contracttest.Expectation{Name: "memos-test", Item: memory.MemoryItem{Text: "contract memory"}, Query: memory.SearchQuery{Text: "contract"}, RefID: "write-1", HitID: "hit-1", HitText: "contract memory"})
}

func TestCloudContract(t *testing.T) {
	client := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/search/memory":
			return jsonResponse(`{"code":200,"data":{"memory_detail_list":[{"memory_id":"hit-1","memory_value":"cloud memory","relativity":0.7}]}}`), nil
		case "/add/message":
			return jsonResponse(`{"code":200,"data":{"message_id":"receipt-1"}}`), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})
	core, err := newProvider("cloud-test", config.ProviderConfig{BaseURL: "https://memos.test", APIKey: "key", UserID: "u"}, cloud, client, func() string { return "generated" })
	if err != nil {
		t.Fatal(err)
	}
	provider := &CloudProvider{provider: core}
	contracttest.Run(t, provider, contracttest.Expectation{Name: "cloud-test", Item: memory.MemoryItem{Text: "cloud memory"}, Query: memory.SearchQuery{Text: "cloud"}, RefID: "receipt-1", HitID: "hit-1", HitText: "cloud memory"})
}

func TestSelfHostedSearchMapsRequestAndResponse(t *testing.T) {
	client := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/product/search" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["user_id"] != "u1" || body["mode"] != "mixture" || body["relativity"] != float64(0) {
			t.Fatalf("unexpected body: %#v", body)
		}
		cubes := body["readable_cube_ids"].([]any)
		if len(cubes) != 1 || cubes[0] != "cube-1" {
			t.Fatalf("unexpected cube scope: %#v", cubes)
		}
		return jsonResponse(`{"code":0,"data":{"text_mem":[{"cube_id":"cube-1","memories":[{"id":"m1","memory":"Todd likes tea","metadata":{"relativity":0.82,"paxm_tier":"ltm"}}]}]}}`), nil
	})
	provider, err := newProvider("local", config.ProviderConfig{BaseURL: "http://memos.test", APIKey: "secret", UserID: "u1", MemCubeID: "cube-1", SearchMode: "mixture"}, selfHosted, client, func() string { return "generated" })
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "drink", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" || hits[0].Text != "Todd likes tea" || hits[0].Score != 0.82 || hits[0].RawScoreKind != "memos_relativity" {
		t.Fatalf("unexpected hits: %#v", hits)
	}
}

func TestCloudUsesTokenAuthAndOpenMemShapes(t *testing.T) {
	requests := 0
	client := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Token cloud-key" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch request.URL.Path {
		case "/api/openmem/v1/search/memory":
			filter, _ := body["filter"].(map[string]any)
			if body["user_id"] != "u1" || filter["agent_id"] != "opencode" || body["memory_limit_number"] != float64(2) {
				t.Fatalf("unexpected search: %#v", body)
			}
			return jsonResponse(`{"data":{"memory_detail_list":[{"memory_id":"m2","memory_value":"uses Go","relativity":0.91,"update_time":"2026-07-12 10:30:00"}]}}`), nil
		case "/api/openmem/v1/add/message":
			if body["user_id"] != "u1" || body["agent_id"] != "opencode" {
				t.Fatalf("unexpected add: %#v", body)
			}
			return jsonResponse(`{"code":0,"message":"ok"}`), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})
	provider, err := newProvider("cloud", config.ProviderConfig{BaseURL: "https://memos.test/api/openmem/v1", APIKey: "cloud-key", UserID: "u1", AgentID: "opencode"}, cloud, client, func() string { return "receipt-1" })
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "language", Limit: 2})
	if err != nil || len(hits) != 1 || hits[0].ID != "m2" || hits[0].Source != "memos-cloud" {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
	ref, err := provider.Put(context.Background(), memory.MemoryItem{Text: "uses Go", Tier: memory.TierLTM})
	if err != nil || ref.ID != "receipt-1" {
		t.Fatalf("ref=%#v err=%v", ref, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestSelfHostedPutAndDelete(t *testing.T) {
	paths := []string{}
	client := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.URL.Path == "/product/add" {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["async_mode"] != "sync" {
				t.Fatalf("add must be synchronous: %#v", body)
			}
			return jsonResponse(`{"code":0,"data":[{"id":"created-1"}]}`), nil
		}
		return jsonResponse(`{"code":0,"data":{"deleted_count":1}}`), nil
	})
	provider, err := newProvider("local", config.ProviderConfig{BaseURL: "http://memos.test", UserID: "u1", MemCubeID: "cube-1"}, selfHosted, client, func() string { return "write" })
	if err != nil {
		t.Fatal(err)
	}
	ref, err := provider.Put(context.Background(), memory.MemoryItem{Text: "remember me"})
	if err != nil || ref.ID != "created-1" {
		t.Fatalf("ref=%#v err=%v", ref, err)
	}
	if err := provider.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/product/add,/product/delete_memory" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestDeleteRejectsBusinessFailure(t *testing.T) {
	client := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"code":200,"message":"not deleted","data":{"status":"failure"}}`), nil
	})
	provider, err := newProvider("local", config.ProviderConfig{BaseURL: "http://memos.test", UserID: "u", MemCubeID: "c"}, selfHosted, client, func() string { return "id" })
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete(context.Background(), memory.MemoryRef{Provider: "local", ID: "m1"}); err == nil {
		t.Fatal("expected business failure")
	}
}

func TestSearchRejectsAPIErrorCode(t *testing.T) {
	client := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"code":500,"message":"backend unavailable"}`), nil
	})
	provider, err := newProvider("local", config.ProviderConfig{BaseURL: "http://memos.test", UserID: "u", MemCubeID: "c"}, selfHosted, client, func() string { return "id" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "query"}); err == nil {
		t.Fatal("expected API envelope error")
	}
}

func TestProviderValidation(t *testing.T) {
	tests := []struct {
		name string
		kind dialect
		cfg  config.ProviderConfig
		want string
	}{
		{"cloud key", cloud, config.ProviderConfig{BaseURL: "https://x.test", UserID: "u"}, "api_key"},
		{"local cube", selfHosted, config.ProviderConfig{BaseURL: "https://x.test", UserID: "u"}, "mem_cube_id"},
		{"user", cloud, config.ProviderConfig{BaseURL: "https://x.test", APIKey: "k"}, "user_id"},
		{"mode", selfHosted, config.ProviderConfig{BaseURL: "https://x.test", UserID: "u", MemCubeID: "c", SearchMode: "magic"}, "search_mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newProvider("p", test.cfg, test.kind, roundTripFunc(nil), func() string { return "x" })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %q", err, test.want)
			}
		})
	}
}

func TestCloudDoesNotAdvertiseDelete(t *testing.T) {
	provider, err := NewCloud("cloud", config.ProviderConfig{BaseURL: "https://memos.test", APIKey: "key", UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(provider).(memory.DeleteProvider); ok {
		t.Fatal("memos cloud must not advertise unreliable delete support")
	}
}
