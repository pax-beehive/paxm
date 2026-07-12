package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestProviderAdapterContract(t *testing.T) {
	provider, err := New("sqlite", filepath.Join(t.TempDir(), "contract.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	contracttest.Run(t, provider, contracttest.Expectation{
		Name: "sqlite", Item: memory.MemoryItem{Text: "cobalt adapter contract", Source: "contract"},
		Query: memory.SearchQuery{Text: "cobalt adapter contract", Limit: 3}, RefID: "", HitID: "", HitText: "cobalt adapter contract",
	})
}

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
	if hit.Tier != memory.TierLTM {
		t.Fatalf("default sqlite tier should be LTM: %#v", hit)
	}
	if hit.RawScore == nil || hit.RawScoreKind != "sqlite_fts_bm25_negated" {
		t.Fatalf("expected sqlite raw score, got %#v", hit)
	}
}

func TestProviderCloseEndsItsDatabaseLifecycle(t *testing.T) {
	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: "before close"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "before"}); err == nil {
		t.Fatal("Search() after Close() succeeded")
	}
}

func TestProviderSearchFiltersTierAndExpiry(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	items := []memory.MemoryItem{
		{Text: "shared tier memory stm", Tier: memory.TierSTM, ExpiresAt: &future},
		{Text: "shared tier memory ltm", Tier: memory.TierLTM},
		{Text: "shared tier memory expired", Tier: memory.TierSTM, ExpiresAt: &expired},
	}
	for _, item := range items {
		if _, err := provider.Put(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:  "shared tier memory",
		Tiers: []memory.MemoryTier{memory.TierSTM},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Tier != memory.TierSTM || !strings.Contains(hits[0].Text, "stm") {
		t.Fatalf("unexpected STM hits: %#v", hits)
	}

	hits, err = provider.Search(context.Background(), memory.SearchQuery{
		Text:  "shared tier memory",
		Tiers: []memory.MemoryTier{memory.TierLTM},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Tier != memory.TierLTM {
		t.Fatalf("unexpected LTM hits: %#v", hits)
	}
}

func TestProviderRejectsLifecycleFingerprintConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		existingFingerprint string
	}{
		{name: "different fingerprint", existingFingerprint: "fingerprint-a"},
		{name: "explicit ID without fingerprint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			existing := memory.MemoryItem{ID: "ltm_fixed", Text: "first durable fact"}
			if tt.existingFingerprint != "" {
				existing.Metadata = map[string]string{
					memory.MetadataFingerprint: tt.existingFingerprint,
					memory.MetadataOccurrences: "1",
				}
			}
			if _, err := provider.Put(context.Background(), existing); err != nil {
				t.Fatal(err)
			}
			incoming := memory.MemoryItem{
				ID:   "ltm_fixed",
				Text: "different durable fact",
				Metadata: map[string]string{
					memory.MetadataFingerprint: "fingerprint-b",
					memory.MetadataOccurrences: "1",
				},
			}
			if _, err := provider.Put(context.Background(), incoming); err == nil || !strings.Contains(err.Error(), "fingerprint conflict") {
				t.Fatalf("conflicting Put() error = %v, want fingerprint conflict", err)
			}
		})
	}
}

func TestProviderCleanupExpiredDeletesRows(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	items := []memory.MemoryItem{
		{Text: "cleanup expired marker", Tier: memory.TierSTM, ExpiresAt: &expired},
		{Text: "cleanup future marker", Tier: memory.TierSTM, ExpiresAt: &future},
		{Text: "cleanup durable marker", Tier: memory.TierLTM},
	}
	for _, item := range items {
		if _, err := provider.Put(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := provider.CleanupExpired(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("CleanupExpired deleted %d rows, want 1", deleted)
	}

	db, err := provider.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM memories WHERE text = ?", "cleanup expired marker").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("expired row still exists: %d", rows)
	}
	var ftsRows int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?", "expired").Scan(&ftsRows); err != nil {
		t.Fatal(err)
	}
	if ftsRows != 0 {
		t.Fatalf("expired FTS row still exists: %d", ftsRows)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:  "cleanup",
		Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := make(map[string]bool, len(hits))
	for _, hit := range hits {
		texts[hit.Text] = true
	}
	if texts["cleanup expired marker"] || !texts["cleanup future marker"] || !texts["cleanup durable marker"] {
		t.Fatalf("unexpected cleanup search hits: %#v", hits)
	}

	deleted, err = provider.CleanupExpired(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("second CleanupExpired deleted %d rows, want 0", deleted)
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

func TestProviderHealthNameAndHelperTables(t *testing.T) {
	t.Parallel()

	t.Run("name and health", func(t *testing.T) {
		provider, err := New("archive", filepath.Join(t.TempDir(), "memory.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if provider.Name() != "archive" {
			t.Fatalf("Name() = %q", provider.Name())
		}
		if err := provider.Health(context.Background()); err != nil {
			t.Fatalf("Health() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := provider.Health(ctx); err == nil {
			t.Fatal("expected canceled health error")
		}
	})

	t.Run("cleanup limit and ids", func(t *testing.T) {
		for limit, want := range map[int]int{-1: 500, 0: 500, 1: 1, 501: 500} {
			if got := cleanupLimit(limit); got != want {
				t.Fatalf("cleanupLimit(%d) = %d, want %d", limit, got, want)
			}
		}
		if id := newID(); strings.TrimSpace(id) == "" {
			t.Fatal("newID returned an empty id")
		}
	})
}
