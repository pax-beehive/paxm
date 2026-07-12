// Package retrieval owns SQLite lexical recall. Its narrow request/result API
// keeps query planning, SQL, scoring, and ranking out of the provider facade.
package retrieval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const RawScoreKind = "sqlite_fts_bm25_negated"

type Request struct {
	Text  string
	Limit int
	Tiers []string
}

type Hit struct {
	ID           string
	Text         string
	Source       string
	Metadata     map[string]string
	CreatedAt    time.Time
	Tier         string
	ExpiresAt    *time.Time
	Score        float64
	RawScore     *float64
	RawScoreKind string
}

func Search(ctx context.Context, db *sql.DB, request Request) ([]Hit, error) {
	terms := normalizeTerms(request.Text)
	if len(terms) == 0 {
		return searchRecent(ctx, db, request)
	}
	return searchFTS(ctx, db, request, terms)
}

func searchRecent(ctx context.Context, db *sql.DB, request Request) ([]Hit, error) {
	filterSQL, filterArgs := filterClause(request, "m")
	args := append(filterArgs, resultLimit(request.Limit))
	rows, err := db.QueryContext(ctx, `
	SELECT m.id, m.text, m.source, m.metadata_json, m.created_at, m.tier, m.expires_at
	FROM memories AS m
	WHERE `+filterSQL+`
	ORDER BY m.created_at DESC
	LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var hits []Hit
	for rows.Next() {
		hit, err := scanHit(rows, nil)
		if err != nil {
			return nil, err
		}
		hit.Score = 0.1
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func searchFTS(ctx context.Context, db *sql.DB, request Request, terms []string) ([]Hit, error) {
	filterSQL, filterArgs := filterClause(request, "m")
	args := append([]any{matchQuery(terms)}, filterArgs...)
	args = append(args, candidateLimit(request.Limit))
	rows, err := db.QueryContext(ctx, `
	SELECT m.id, m.text, m.source, m.metadata_json, m.created_at, m.tier, m.expires_at, bm25(memory_fts) AS rank
	FROM memory_fts
	JOIN memories AS m ON m.rowid = memory_fts.rowid
	WHERE memory_fts MATCH ? AND `+filterSQL+`
	ORDER BY rank ASC, m.created_at DESC
	LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var hits []Hit
	for rows.Next() {
		var rank sql.NullFloat64
		hit, err := scanHit(rows, &rank)
		if err != nil {
			return nil, err
		}
		if rank.Valid {
			rawScore := -rank.Float64
			hit.RawScore = &rawScore
			hit.RawScoreKind = RawScoreKind
		}
		hit.Score = score(terms, hit.Text)
		if hit.Score > 0 {
			hits = append(hits, hit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sortHits(hits)
	if request.Limit > 0 && len(hits) > request.Limit {
		hits = hits[:request.Limit]
	}
	return hits, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHit(rows rowScanner, rank *sql.NullFloat64) (Hit, error) {
	var hit Hit
	var metadataJSON, createdAt, expiresAt string
	dest := []any{&hit.ID, &hit.Text, &hit.Source, &metadataJSON, &createdAt, &hit.Tier, &expiresAt}
	if rank != nil {
		dest = append(dest, rank)
	}
	if err := rows.Scan(dest...); err != nil {
		return Hit{}, err
	}
	if strings.TrimSpace(metadataJSON) != "" && metadataJSON != "{}" {
		if err := json.Unmarshal([]byte(metadataJSON), &hit.Metadata); err != nil {
			return Hit{}, err
		}
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Hit{}, fmt.Errorf("sqlite memory %q created_at: %w", hit.ID, err)
	}
	hit.CreatedAt = created
	if strings.TrimSpace(expiresAt) != "" {
		expires, err := time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			return Hit{}, fmt.Errorf("sqlite memory %q expires_at: %w", hit.ID, err)
		}
		hit.ExpiresAt = &expires
	}
	return hit, nil
}

func filterClause(request Request, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	clauses := []string{"(" + prefix + "expires_at = '' OR " + prefix + "expires_at > ?)"}
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}
	if len(request.Tiers) > 0 {
		placeholders := make([]string, 0, len(request.Tiers))
		for _, tier := range request.Tiers {
			placeholders = append(placeholders, "?")
			args = append(args, tier)
		}
		clauses = append(clauses, prefix+"tier IN ("+strings.Join(placeholders, ", ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

func matchQuery(terms []string) string {
	unique := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		unique = append(unique, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(unique, " OR ")
}

func normalizeTerms(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := fields[:0]
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

func score(queryTerms []string, text string) float64 {
	if len(queryTerms) == 0 {
		return 0.1
	}
	textTerms := normalizeTerms(text)
	if len(textTerms) == 0 {
		return 0
	}
	if strings.Contains(" "+strings.Join(textTerms, " ")+" ", " "+strings.Join(queryTerms, " ")+" ") {
		return 1
	}
	textSet := make(map[string]struct{}, len(textTerms))
	for _, term := range textTerms {
		textSet[term] = struct{}{}
	}
	seenQuery := make(map[string]struct{}, len(queryTerms))
	matched := 0
	for _, term := range queryTerms {
		if _, seen := seenQuery[term]; seen {
			continue
		}
		seenQuery[term] = struct{}{}
		if _, ok := textSet[term]; ok {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(seenQuery))
}

func sortHits(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if rawGreater(hits[i], hits[j]) {
			return true
		}
		if rawGreater(hits[j], hits[i]) {
			return false
		}
		return hits[i].CreatedAt.After(hits[j].CreatedAt)
	})
}

func resultLimit(limit int) int {
	if limit > 0 {
		return limit
	}
	return 50
}

func candidateLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit*5 > 50 {
		return limit * 5
	}
	return 50
}

func rawGreater(left, right Hit) bool {
	return left.RawScore != nil && right.RawScore != nil && *left.RawScore > *right.RawScore
}
