package retrieval

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSearchOwnsSQLiteRecallBehavior(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	now := time.Now().UTC()
	insertTestMemory(t, db, "ltm", "passive recall exact phrase", "hook", `{"workspace":"alpha"}`, now.Add(-time.Minute), "ltm", "")
	insertTestMemory(t, db, "stm", "passive recall partial", "hook", `{}`, now, "stm", now.Add(time.Hour).Format(time.RFC3339Nano))
	insertTestMemory(t, db, "expired", "passive recall expired", "hook", `{}`, now.Add(time.Minute), "stm", now.Add(-time.Hour).Format(time.RFC3339Nano))

	hits, err := Search(context.Background(), db, Request{Text: "passive recall", Limit: 5, Tiers: []string{"ltm"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "ltm" || hits[0].Score != 1 || hits[0].Metadata["workspace"] != "alpha" {
		t.Fatalf("FTS hits = %#v", hits)
	}
	if hits[0].RawScore == nil || hits[0].RawScoreKind != RawScoreKind {
		t.Fatalf("FTS raw score = %#v", hits[0])
	}

	hits, err = Search(context.Background(), db, Request{Limit: 10, Tiers: []string{"stm"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "stm" || hits[0].Score != 0.1 || hits[0].ExpiresAt == nil {
		t.Fatalf("recent hits = %#v", hits)
	}
}

func TestSearchExtractsLongQueryFocusedRecallContext(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	lines := []string{"[9 June 2023]"}
	for i := 0; i < 90; i++ {
		lines = append(lines, "Caroline: my current group of friends is supportive")
	}
	lines = append(lines, "Caroline: we have known each other for 4 years")
	for i := 0; i < 90; i++ {
		lines = append(lines, "Caroline: my current group of friends is supportive")
	}
	original := strings.Join(lines, "\n")
	insertTestMemory(t, db, "session", original, "locomo", `{}`, time.Now().UTC(), "ltm", "")

	hits, err := Search(context.Background(), db, Request{Text: "How long has Caroline had her current group of friends for?", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search() hits = %#v", hits)
	}
	if !strings.Contains(hits[0].Text, "[9 June 2023]") || !strings.Contains(hits[0].Text, "4 years") {
		t.Fatalf("excerpt lost temporal context: %q", hits[0].Text)
	}
	if len(hits[0].Text) >= len(original) || hits[0].Metadata["sqlite_excerpted"] != "true" {
		t.Fatalf("long result was not excerpted: %#v", hits[0])
	}
}

func TestSearchExtractsLongUnspacedCJKRecallContext(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	target := "部署区域是美国西部二区"
	original := strings.Repeat("背景资料", 400) + target + strings.Repeat("历史记录", 400)
	insertTestMemory(t, db, "cjk", original, "test", `{}`, time.Now().UTC(), "ltm", "")

	hits, err := Search(context.Background(), db, Request{Text: "部署区域", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, target) {
		t.Fatalf("CJK excerpt lost target: %#v", hits)
	}
	if len(hits[0].Text) >= len(original) {
		t.Fatalf("long CJK result was not excerpted: %d >= %d", len(hits[0].Text), len(original))
	}
}

func TestSearchUsesLightweightAnalyzerForLexicalCandidates(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	now := time.Now().UTC()
	items := []struct{ id, text string }{
		{"camel", "providerSearchTimeout is enabled"},
		{"cjk", "生产环境部署区域是美国西部一区"},
		{"morphology", "deployment application decision"},
		{"alias", "repository migration decision"},
	}
	for i, item := range items {
		insertTestMemory(t, db, item.id, item.text, "test", `{}`, now.Add(time.Duration(i)*time.Second), "ltm", "")
	}
	queries := map[string]string{
		"provider search timeout": "camel",
		"部署区域":                    "cjk",
		"deploy application":      "morphology",
		"repo migration":          "alias",
		"deploy":                  "morphology",
		"repo":                    "alias",
	}
	for query, wantID := range queries {
		t.Run(query, func(t *testing.T) {
			hits, err := Search(context.Background(), db, Request{Text: query, Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 || hits[0].ID != wantID {
				t.Fatalf("Search(%q) = %#v, want %s", query, hits, wantID)
			}
		})
	}
}

func TestSearchKeepsCompleteIdentifierBeyondFTSCandidateLimit(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	now := time.Now().UTC()
	insertTestMemory(t, db, "target", "providerSearchTimeout is enabled", "test", `{}`, now.Add(-time.Hour), "ltm", "")
	for i := 0; i < 75; i++ {
		insertTestMemory(t, db, "noise-"+strconv.Itoa(i), "provider note "+strconv.Itoa(i), "test", `{}`, now.Add(time.Duration(i)*time.Second), "ltm", "")
	}
	hits, err := Search(context.Background(), db, Request{Text: "provider search timeout", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "target" {
		t.Fatalf("identifier pressure hits = %#v", hits)
	}
}

func TestSearchDoesNotLetExactHitHideExpandedHit(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	now := time.Now().UTC()
	insertTestMemory(t, db, "exact", "repo", "test", `{}`, now, "ltm", "")
	insertTestMemory(t, db, "expanded", "repository migration", "test", `{}`, now.Add(-time.Second), "ltm", "")
	hits, err := Search(context.Background(), db, Request{Text: "repo", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("mixed exact and expanded hits = %#v", hits)
	}
}

func TestExecutePlanTraceShowsExplicitStageChoice(t *testing.T) {
	t.Parallel()
	db := openTestDatabase(t)
	now := time.Now().UTC()
	insertTestMemory(t, db, "exact", "decision: provider timeout bulkhead", "test", `{}`, now, "ltm", "")
	insertTestMemory(t, db, "partial-a", "provider note", "test", `{}`, now.Add(time.Second), "ltm", "")
	insertTestMemory(t, db, "partial-b", "timeout note", "test", `{}`, now.Add(2*time.Second), "ltm", "")

	request := Request{Text: "provider timeout bulkhead", Limit: 5}
	result, err := executePlan(context.Background(), db, request, analyze(request.Text))
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Selected != stageExact || result.Trace.Exact != 1 || result.Trace.Relaxed != 2 {
		t.Fatalf("exact trace = %#v", result.Trace)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "exact" {
		t.Fatalf("exact stage hits = %#v", result.Hits)
	}

	request.Text = "bulkhead provider timeout"
	result, err = executePlan(context.Background(), db, request, analyze(request.Text))
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Selected != stageStrict || result.Trace.Exact != 0 || result.Trace.Strict != 1 || result.Trace.Relaxed != 2 {
		t.Fatalf("strict trace = %#v", result.Trace)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "exact" {
		t.Fatalf("strict stage hits = %#v", result.Hits)
	}

	request.Text = "provider timeout rollback"
	result, err = executePlan(context.Background(), db, request, analyze(request.Text))
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Selected != stageRelaxed || result.Trace.Exact != 0 || result.Trace.Strict != 0 || result.Trace.Relaxed == 0 {
		t.Fatalf("relaxed trace = %#v", result.Trace)
	}
}

func TestFuseCandidateListsUsesReciprocalRankFusion(t *testing.T) {
	t.Parallel()
	first := Hit{ID: "first"}
	shared := Hit{ID: "shared"}
	last := Hit{ID: "last"}
	fused := fuseCandidateLists([]Hit{first, shared}, []Hit{last, shared})
	byID := make(map[string]plannedHit, len(fused))
	for _, hit := range fused {
		byID[hit.ID] = hit
	}
	if byID["shared"].rrf <= byID["first"].rrf || byID["shared"].rrf <= byID["last"].rrf {
		t.Fatalf("RRF scores = shared %f, first %f, last %f", byID["shared"].rrf, byID["first"].rrf, byID["last"].rrf)
	}
}

func TestAnalyzeSplitsIdentifiersAndCanonicalizesBoundedVocabulary(t *testing.T) {
	t.Parallel()
	tests := map[string][]string{
		"providerSearchTimeout":       {"provider", "search", "timeout"},
		"PAXM_INSTALL_DIR":            {"paxm", "install", "dir"},
		"workspace_filter_enabled":    {"workspace", "filter", "enabled"},
		"internal/provider/search.go": {"internal", "provider", "search", "go"},
		"deployment retries config":   {"deploy", "retry", "config"},
		"数据库迁移":                       {"数据库迁移"},
	}
	for input, want := range tests {
		if got := analyze(input).canonicalTerms(); !reflect.DeepEqual(got, want) {
			t.Errorf("analyze(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE memories (rowid INTEGER PRIMARY KEY, id TEXT, text TEXT, source TEXT, metadata_json TEXT, created_at TEXT, tier TEXT, expires_at TEXT)`,
		`CREATE VIRTUAL TABLE memory_fts USING fts5(text, source, content='memories', content_rowid='rowid', tokenize='unicode61')`,
		`CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN INSERT INTO memory_fts(rowid, text, source) VALUES (new.rowid, new.text, new.source); END`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertTestMemory(t *testing.T, db *sql.DB, id, text, source, metadata string, created time.Time, tier, expires string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO memories(id, text, source, metadata_json, created_at, tier, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, text, source, metadata, created.Format(time.RFC3339Nano), tier, expires); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeTermsAndScorePreserveLegacyBehavior(t *testing.T) {
	t.Parallel()
	if got, want := normalizeTerms("PAXM, memory! 召回"), []string{"paxm", "memory", "召回"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeTerms() = %#v, want %#v", got, want)
	}
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
			if got := score(tt.query, tt.text); got != tt.want {
				t.Fatalf("score() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLegacyLimitsAndRanking(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ limit, result, candidate int }{{0, 50, 50}, {3, 3, 50}, {20, 20, 100}} {
		t.Run(strconv.Itoa(tt.limit), func(t *testing.T) {
			if got := resultLimit(tt.limit); got != tt.result {
				t.Fatalf("resultLimit() = %d, want %d", got, tt.result)
			}
			if got := candidateLimit(tt.limit); got != tt.candidate {
				t.Fatalf("candidateLimit() = %d, want %d", got, tt.candidate)
			}
		})
	}
	left, right := 0.7, 0.6
	if !rawGreater(Hit{RawScore: &left}, Hit{RawScore: &right}) || rawGreater(Hit{}, Hit{RawScore: &right}) {
		t.Fatal("rawGreater changed legacy nil or score ordering")
	}
	hits := []plannedHit{{Hit: Hit{ID: "old", Score: 1, CreatedAt: time.Unix(1, 0)}}, {Hit: Hit{ID: "new", Score: 1, CreatedAt: time.Unix(2, 0)}}}
	sortPlannedHits(hits)
	if got := strings.Join([]string{hits[0].ID, hits[1].ID}, ","); got != "new,old" {
		t.Fatalf("sortPlannedHits() order = %s", got)
	}
}

func TestMatchQueryDeduplicatesAndEscapesTerms(t *testing.T) {
	t.Parallel()
	if got, want := matchQuery([]string{"paxm", "paxm", `a"b`}), `"paxm" OR "a""b"`; got != want {
		t.Fatalf("matchQuery() = %q, want %q", got, want)
	}
}
