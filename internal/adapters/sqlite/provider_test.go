package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

func TestProviderPutAndSearch(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := provider.Put(context.Background(), memory.MemoryItem{
		Text:   "adapter registry fans out recall across enabled providers",
		Source: "test",
		Metadata: map[string]string{
			"agent": "codex",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provider != "sqlite" || ref.ID == "" {
		t.Fatalf("unexpected ref: %#v", ref)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:  "enabled providers",
		Limit: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected one hit, got %d", len(hits))
	}
	hit := hits[0]
	if hit.Text == "" || hit.Score == 0 || hit.Relevance == 0 {
		t.Fatalf("unexpected hit: %#v", hit)
	}
	if hit.Source != "test" || hit.Metadata["agent"] != "codex" {
		t.Fatalf("metadata/source did not round trip: %#v", hit)
	}
	if hit.RawScore == nil || hit.RawScoreKind != "sqlite_fts_bm25_negated" {
		t.Fatalf("expected sqlite raw score, got %#v", hit)
	}
}

func TestProviderSearchHandlesPunctuationHeavyQueries(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Put(context.Background(), memory.MemoryItem{
		Text: "PAXM_REAL_PI_E2E_VISIBLE_7319: Pi agent passive recall should surface this.",
	})
	if err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:  "PAXM_REAL_PI_E2E_VISIBLE_7319?",
		Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Relevance != 1 {
		t.Fatalf("expected exact punctuation-insensitive hit, got %#v", hits)
	}
}

func TestProviderEmptyQueryReturnsRecentMemories(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first memory", "second memory"} {
		if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: text}); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Score != 0.1 {
		t.Fatalf("unexpected recent hits: %#v", hits)
	}
}
