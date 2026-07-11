package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if hit.Tier != memory.TierLTM {
		t.Fatalf("default sqlite tier should be LTM: %#v", hit)
	}
	if hit.RawScore == nil || hit.RawScoreKind != "sqlite_fts_bm25_negated" {
		t.Fatalf("expected sqlite raw score, got %#v", hit)
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

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	item := memory.MemoryItem{
		ID:   "ltm_fixed",
		Text: "first durable fact",
		Metadata: map[string]string{
			memory.MetadataFingerprint: "fingerprint-a",
			memory.MetadataOccurrences: "1",
		},
	}
	if _, err := provider.Put(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	item.Text = "different durable fact"
	item.Metadata[memory.MetadataFingerprint] = "fingerprint-b"
	if _, err := provider.Put(context.Background(), item); err == nil || !strings.Contains(err.Error(), "fingerprint conflict") {
		t.Fatalf("conflicting Put() error = %v, want fingerprint conflict", err)
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

	t.Run("terms and scores", func(t *testing.T) {
		tests := []struct {
			name  string
			query []string
			text  string
			want  float64
		}{
			{name: "empty query", text: "anything", want: 0.1},
			{name: "empty text", query: []string{"paxm"}, want: 0},
			{name: "exact phrase", query: []string{"passive", "recall"}, text: "before passive recall after", want: 1},
			{name: "unique term ratio", query: []string{"paxm", "paxm", "memory"}, text: "paxm only", want: 0.5},
			{name: "no terms match", query: []string{"zep"}, text: "sqlite memory", want: 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := scoreMemory(tt.query, tt.text); got != tt.want {
					t.Fatalf("scoreMemory() = %v, want %v", got, tt.want)
				}
			})
		}
		if got, want := normalizeTerms("PAXM, memory! 召回"), []string{"paxm", "memory", "召回"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("normalizeTerms() = %#v, want %#v", got, want)
		}
	})

	t.Run("limits raw score and ids", func(t *testing.T) {
		limitTests := []struct {
			limit         int
			wantProvider  int
			wantCandidate int
		}{
			{limit: 0, wantProvider: 50, wantCandidate: 50},
			{limit: 3, wantProvider: 3, wantCandidate: 50},
			{limit: 20, wantProvider: 20, wantCandidate: 100},
		}
		for _, tt := range limitTests {
			t.Run(strings.Join([]string{"limit", strconv.Itoa(tt.limit)}, "-"), func(t *testing.T) {
				if got := providerLimit(tt.limit); got != tt.wantProvider {
					t.Fatalf("providerLimit() = %d, want %d", got, tt.wantProvider)
				}
				if got := candidateLimit(tt.limit); got != tt.wantCandidate {
					t.Fatalf("candidateLimit() = %d, want %d", got, tt.wantCandidate)
				}
			})
		}
		leftScore := 0.7
		rightScore := 0.6
		if !rawGreater(memory.MemoryHit{RawScore: &leftScore}, memory.MemoryHit{RawScore: &rightScore}) {
			t.Fatal("rawGreater should compare raw scores")
		}
		if rawGreater(memory.MemoryHit{}, memory.MemoryHit{RawScore: &rightScore}) {
			t.Fatal("rawGreater should be false when a raw score is missing")
		}
		if id := newID(); strings.TrimSpace(id) == "" {
			t.Fatal("newID returned an empty id")
		}
	})
}
