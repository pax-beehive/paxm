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
	hits := []Hit{{ID: "old", Score: 1, CreatedAt: time.Unix(1, 0)}, {ID: "new", Score: 1, CreatedAt: time.Unix(2, 0)}}
	sortHits(hits)
	if got := strings.Join([]string{hits[0].ID, hits[1].ID}, ","); got != "new,old" {
		t.Fatalf("sortHits() order = %s", got)
	}
}

func TestMatchQueryDeduplicatesAndEscapesTerms(t *testing.T) {
	t.Parallel()
	if got, want := matchQuery([]string{"paxm", "paxm", `a"b`}), `"paxm" OR "a""b"`; got != want {
		t.Fatalf("matchQuery() = %q, want %q", got, want)
	}
}
