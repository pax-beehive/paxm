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
)

const RawScoreKind = "sqlite_fts_bm25_negated"

type Request struct {
	Text      string
	Limit     int
	Tiers     []string
	Workspace string
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

type planStage string

const (
	stageExact   planStage = "exact_phrase"
	stageStrict  planStage = "strict_all_terms"
	stageRelaxed planStage = "relaxed_partial"
)

type branchTrace struct {
	Name       string
	Candidates int
}

type searchTrace struct {
	Branches []branchTrace
	Exact    int
	Strict   int
	Relaxed  int
	Selected planStage
}

type planResult struct {
	Hits  []Hit
	Trace searchTrace
}

type plannedHit struct {
	Hit
	rrf     float64
	density float64
}

func Search(ctx context.Context, db *sql.DB, request Request) ([]Hit, error) {
	terms := analyze(request.Text)
	if len(terms) == 0 {
		return searchRecent(ctx, db, request)
	}
	result, err := executePlan(ctx, db, request, terms)
	return result.Hits, err
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

func executePlan(ctx context.Context, db *sql.DB, request Request, terms analysis) (planResult, error) {
	filterSQL, filterArgs := filterClause(request, "m")
	args := append([]any{matchQuery(terms.canonicalTerms())}, filterArgs...)
	args = append(args, candidateLimit(request.Limit))
	ftsHits, err := queryCandidates(ctx, db, `
	SELECT m.id, m.text, m.source, m.metadata_json, m.created_at, m.tier, m.expires_at, bm25(memory_fts) AS rank
	FROM memory_fts
	JOIN memories AS m ON m.rowid = memory_fts.rowid
	WHERE memory_fts MATCH ? AND `+filterSQL+`
	ORDER BY rank ASC, m.created_at DESC
	LIMIT ?
	`, true, args...)
	if err != nil {
		return planResult{}, err
	}
	trace := searchTrace{Branches: []branchTrace{{Name: "fts_or", Candidates: len(ftsHits)}}}
	lists := [][]Hit{ftsHits}
	queryTerms := terms.canonicalTerms()
	if terms.needsSupplement(request.Text) && !hasEnoughPhraseMatches(ftsHits, request.Text, queryTerms, resultLimit(request.Limit)) {
		likeSQL, likeArgs := likeClause(terms, "m")
		likeArgs = append(likeArgs, filterArgs...)
		likeArgs = append(likeArgs, candidateLimit(request.Limit))
		likeHits, err := queryCandidates(ctx, db, `
	SELECT m.id, m.text, m.source, m.metadata_json, m.created_at, m.tier, m.expires_at
	FROM memories AS m
	WHERE (`+likeSQL+`) AND `+filterSQL+`
	ORDER BY m.created_at DESC
	LIMIT ?
		`, false, likeArgs...)
		if err != nil {
			return planResult{}, err
		}
		trace.Branches = append(trace.Branches, branchTrace{Name: "lexical_all_terms", Candidates: len(likeHits)})
		lists = append(lists, likeHits)
	}
	selected := selectStage(fuseCandidateLists(lists...), request.Text, queryTerms, &trace)
	sortPlannedHits(selected)
	limit := resultLimit(request.Limit)
	if len(selected) > limit {
		selected = selected[:limit]
	}
	hits := make([]Hit, len(selected))
	for i := range selected {
		hits[i] = selected[i].Hit
	}
	return planResult{Hits: hits, Trace: trace}, nil
}

func fuseCandidateLists(lists ...[]Hit) []plannedHit {
	const rrfK = 60.0
	byID := make(map[string]int)
	result := make([]plannedHit, 0)
	for _, list := range lists {
		for rank, hit := range list {
			index, ok := byID[hit.ID]
			if !ok {
				index = len(result)
				byID[hit.ID] = index
				result = append(result, plannedHit{Hit: hit})
			} else if result[index].RawScore == nil && hit.RawScore != nil {
				result[index].RawScore = hit.RawScore
				result[index].RawScoreKind = hit.RawScoreKind
			}
			result[index].rrf += 1 / (rrfK + float64(rank+1))
		}
	}
	return result
}

func selectStage(candidates []plannedHit, query string, queryTerms []string, trace *searchTrace) []plannedHit {
	exact := make([]plannedHit, 0)
	strict := make([]plannedHit, 0)
	relaxed := make([]plannedHit, 0)
	uniqueTerms := uniqueStrings(queryTerms)
	for _, candidate := range candidates {
		matched := matchedTermCount(uniqueTerms, candidate.Text)
		if matched == 0 {
			continue
		}
		candidate.density = evidenceDensity(matched, candidate.Text)
		switch {
		case containsPhrase(query, uniqueTerms, candidate.Text):
			candidate.Score = 1
			exact = append(exact, candidate)
		case matched == len(uniqueTerms):
			candidate.Score = 1
			strict = append(strict, candidate)
		default:
			candidate.Score = float64(matched) / float64(len(uniqueTerms))
			relaxed = append(relaxed, candidate)
		}
	}
	trace.Exact, trace.Strict, trace.Relaxed = len(exact), len(strict), len(relaxed)
	switch {
	case len(exact) > 0:
		trace.Selected = stageExact
		return exact
	case len(strict) > 0:
		trace.Selected = stageStrict
		return strict
	default:
		trace.Selected = stageRelaxed
		return relaxed
	}
}

func containsPhrase(query string, queryTerms []string, text string) bool {
	if strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query))) {
		return true
	}
	textTerms := normalizeTerms(text)
	return strings.Contains(" "+strings.Join(textTerms, " ")+" ", " "+strings.Join(queryTerms, " ")+" ")
}

func matchedTermCount(queryTerms []string, text string) int {
	textTerms := normalizeTerms(text)
	textSet := make(map[string]struct{}, len(textTerms))
	for _, term := range textTerms {
		textSet[term] = struct{}{}
	}
	lowerText := strings.ToLower(text)
	matched := 0
	for _, term := range queryTerms {
		if _, ok := textSet[term]; ok || strings.Contains(lowerText, term) {
			matched++
		}
	}
	return matched
}

func evidenceDensity(matched int, text string) float64 {
	terms := uniqueStrings(normalizeTerms(text))
	if len(terms) == 0 {
		return 0
	}
	return float64(matched) / float64(len(terms))
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sortPlannedHits(hits []plannedHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].density != hits[j].density {
			return hits[i].density > hits[j].density
		}
		if hits[i].rrf != hits[j].rrf {
			return hits[i].rrf > hits[j].rrf
		}
		if rawGreater(hits[i].Hit, hits[j].Hit) {
			return true
		}
		if rawGreater(hits[j].Hit, hits[i].Hit) {
			return false
		}
		return hits[i].CreatedAt.After(hits[j].CreatedAt)
	})
}

func hasEnoughPhraseMatches(hits []Hit, query string, queryTerms []string, required int) bool {
	exact := 0
	for _, hit := range hits {
		if containsPhrase(query, queryTerms, hit.Text) {
			exact++
			if exact >= required {
				return true
			}
		}
	}
	return false
}

func queryCandidates(ctx context.Context, db *sql.DB, statement string, ranked bool, args ...any) ([]Hit, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hits []Hit
	for rows.Next() {
		var rank sql.NullFloat64
		var rankTarget *sql.NullFloat64
		if ranked {
			rankTarget = &rank
		}
		hit, err := scanHit(rows, rankTarget)
		if err != nil {
			return nil, err
		}
		if rank.Valid {
			rawScore := -rank.Float64
			hit.RawScore = &rawScore
			hit.RawScoreKind = RawScoreKind
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
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
	if request.Workspace != "" {
		clauses = append(clauses, "COALESCE(json_extract("+prefix+"metadata_json, '$.workspace'), '') IN ('', ?)")
		args = append(args, request.Workspace)
	}
	return strings.Join(clauses, " AND "), args
}

func likeClause(terms analysis, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms))
	for _, term := range terms {
		variants := make([]string, 0, len(term.patterns))
		for _, pattern := range term.patterns {
			variants = append(variants, "lower("+prefix+"text) LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(pattern)+"%")
		}
		clauses = append(clauses, "("+strings.Join(variants, " OR ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
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
	return analyze(text).canonicalTerms()
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
	lowerText := strings.ToLower(text)
	for _, term := range queryTerms {
		if _, seen := seenQuery[term]; seen {
			continue
		}
		seenQuery[term] = struct{}{}
		if _, ok := textSet[term]; ok || strings.Contains(lowerText, term) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(seenQuery))
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
