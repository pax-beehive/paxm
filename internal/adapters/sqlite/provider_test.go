package sqlite

import (
	"context"
	"fmt"
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

func TestProviderSearchExtractsRelevantContextFromLongSQLiteMemory(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	lines := make([]string, 0, 64)
	for i := 0; i < 30; i++ {
		lines = append(lines, "Morgan: unrelated planning notes that should not consume recall context")
	}
	lines = append(lines,
		"Riley: the deployment discussion starts here",
		"Morgan: the atlas deployment region is us-west-2",
		"Riley: keep the rollback in us-east-1",
	)
	for i := 0; i < 30; i++ {
		lines = append(lines, "Morgan: unrelated retrospective notes that should not consume recall context")
	}
	original := strings.Join(lines, "\n")
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: original, Source: "session"}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected one hit, got %d", len(hits))
	}
	hit := hits[0]
	for _, want := range []string{
		"Riley: the deployment discussion starts here",
		"Morgan: the atlas deployment region is us-west-2",
		"Riley: keep the rollback in us-east-1",
	} {
		if !strings.Contains(hit.Text, want) {
			t.Fatalf("excerpt missing %q:\n%s", want, hit.Text)
		}
	}
	if len(hit.Text) >= len(original) {
		t.Fatalf("long SQLite memory was not shortened: got %d bytes, original %d", len(hit.Text), len(original))
	}
	if hit.Metadata["sqlite_excerpted"] != "true" {
		t.Fatalf("excerpt metadata = %#v", hit.Metadata)
	}
}

func TestProviderSearchLeavesShortSQLiteMemoryUnchanged(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	original := "Morgan: the atlas deployment region is us-west-2"
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: original}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Text != original {
		t.Fatalf("short SQLite memory changed: %#v", hits)
	}
	if hits[0].Metadata["sqlite_excerpted"] != "" {
		t.Fatalf("short SQLite memory marked as excerpted: %#v", hits[0].Metadata)
	}
}

func TestProviderSearchReturnsNoHitsWithoutExcerptPanic(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: "unrelated stored memory"}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("unexpected hits: %#v", hits)
	}
}

func TestProviderSearchExtractsEvidenceFromLongSingleLineMemory(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	prefix := strings.Repeat("Old planning detail without a final location. ", 40)
	target := "The atlas deployment region is us-west-2. "
	suffix := strings.Repeat("Unrelated retrospective detail after the decision. ", 40)
	original := "Morgan: " + prefix + target + suffix
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: original}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, strings.TrimSpace(target)) {
		t.Fatalf("single-line excerpt lost target evidence: %#v", hits)
	}
	if len(hits[0].Text) >= len(original) {
		t.Fatalf("single-line SQLite memory was not shortened: got %d bytes, original %d", len(hits[0].Text), len(original))
	}
}

func TestProviderSearchExtractsEvidenceFromLongUnspacedCJKMemory(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	target := "部署区域是美国西部二区"
	original := strings.Repeat("背景资料", 300) + target + strings.Repeat("历史记录", 300)
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: original}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "部署区域", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, target) {
		t.Fatalf("CJK excerpt lost target evidence: %#v", hits)
	}
	if len(hits[0].Text) >= len(original) {
		t.Fatalf("long CJK memory was not shortened: got %d bytes, original %d", len(hits[0].Text), len(original))
	}
}

func TestProviderSearchKeepsBestEvidenceUnderExcerptBudget(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	longFiller := strings.Repeat(" background", 100)
	original := strings.Join([]string{
		"Morgan: atlas" + longFiller,
		"Riley: context before the first partial match" + longFiller,
		"Morgan: deployment" + longFiller,
		"Riley: context after the second partial match" + longFiller,
		"Morgan: the atlas deployment region is us-west-2",
	}, "\n")
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: original}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "the atlas deployment region is us-west-2") {
		t.Fatalf("best evidence was displaced by lower-quality segments:\n%#v", hits)
	}
}

func TestProviderSearchBoundsCombinedLongSQLiteContext(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	for i := 0; i < 5; i++ {
		text := strings.Join([]string{
			fmt.Sprintf("Morgan: atlas deployment region %d is us-west-2 %s", i, strings.Repeat("supporting detail ", 100)),
			"Riley: " + strings.Repeat("unrelated historical context ", 100),
		}, "\n")
		if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: text}); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "atlas deployment region", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatalf("expected five hits, got %d", len(hits))
	}
	totalBytes := 0
	for _, hit := range hits {
		totalBytes += len(hit.Text)
		if !strings.Contains(hit.Text, "atlas deployment region") {
			t.Fatalf("budgeted hit lost evidence: %#v", hit)
		}
	}
	if totalBytes > 8000 {
		t.Fatalf("combined SQLite context = %d bytes, want <= 8000", totalBytes)
	}
}

func TestProviderSearchExcerptIgnoresQuestionStopWords(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	lines := make([]string, 0, 62)
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: I did give support at a community gathering with friends")
	}
	lines = append(lines, "Caroline: My school event was last week")
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: I did give support at a community gathering with friends")
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: strings.Join(lines, "\n")}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "When did Caroline give a speech at a school?", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "My school event was last week") {
		t.Fatalf("question stop words displaced the relevant evidence:\n%#v", hits)
	}
}

func TestProviderSearchExcerptUsesBoundedSpeechVocabulary(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	lines := make([]string, 0, 62)
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: I give a speech")
	}
	lines = append(lines, "Caroline: I talked about my journey at the school event last week and encouraged the students")
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: I give a speech")
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: strings.Join(lines, "\n")}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "When did Caroline give a speech at a school?", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "talked about my journey") {
		t.Fatalf("speech vocabulary did not preserve the relevant evidence:\n%#v", hits)
	}
}

func TestProviderSearchExcerptKeepsSessionTimestamp(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	lines := []string{"[9 June 2023]"}
	for i := 0; i < 40; i++ {
		lines = append(lines, "Caroline: unrelated details about the community gathering")
	}
	lines = append(lines, "Caroline: I talked at the school event last week")
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: strings.Join(lines, "\n")}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "When did Caroline give a speech at a school?", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "[9 June 2023]") {
		t.Fatalf("session timestamp was omitted from the excerpt:\n%#v", hits)
	}
}

func TestProviderSearchExcerptPrioritizesTemporalEvidenceForDurationQuestion(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	lines := make([]string, 0, 62)
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: my current group of friends is supportive")
	}
	lines = append(lines, "Caroline: we have known each other for 4 years")
	for i := 0; i < 30; i++ {
		lines = append(lines, "Caroline: my current group of friends is supportive")
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: strings.Join(lines, "\n")}); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "How long has Caroline had her current group of friends for?", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "4 years") {
		t.Fatalf("duration evidence was displaced by lexical distractors:\n%#v", hits)
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

func TestProviderSearchHardFiltersWorkspace(t *testing.T) {
	t.Parallel()

	provider, err := New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []memory.MemoryItem{
		{Text: "atlas deployment region us-west-1", Metadata: map[string]string{"workspace": "/work/atlas"}},
		{Text: "atlas deployment region us-east-1", Metadata: map[string]string{"workspace": "/work/other"}},
		{Text: "atlas deployment region global", Metadata: nil},
	} {
		if _, err := provider.Put(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text: "atlas deployment region", Limit: 5, Metadata: map[string]string{"workspace": "/work/atlas"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("workspace-filtered hits = %#v", hits)
	}
	for _, hit := range hits {
		if hit.Metadata["workspace"] == "/work/other" {
			t.Fatalf("cross-workspace hit leaked: %#v", hit)
		}
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
