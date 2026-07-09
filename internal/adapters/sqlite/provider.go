package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pax-beehive/memory-adaptor/internal/memory"
	_ "modernc.org/sqlite"
)

type Provider struct {
	name string
	path string
}

func New(name, path string) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("sqlite provider name is required")
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite provider path is required")
	}
	return &Provider{name: name, path: path}, nil
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := p.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	terms := normalizeTerms(query.Text)
	if len(terms) == 0 {
		return p.searchRecent(ctx, db, query.Limit)
	}
	return p.searchFTS(ctx, db, query, terms)
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	refs, err := p.PutBatch(ctx, []memory.MemoryItem{item})
	if err != nil {
		return memory.MemoryRef{}, err
	}
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("sqlite provider did not store memory")
	}
	return refs[0], nil
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	for i := range items {
		if strings.TrimSpace(items[i].Text) == "" {
			return nil, errors.New("memory text is required")
		}
		if items[i].ID == "" {
			items[i].ID = newID()
		}
		if items[i].CreatedAt.IsZero() {
			items[i].CreatedAt = now
		}
	}

	db, err := p.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	refs := make([]memory.MemoryRef, 0, len(items))
	for _, item := range items {
		metadata, err := encodeMetadata(item.Metadata)
		if err != nil {
			return refs, err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO memories(id, text, source, metadata_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  text = excluded.text,
  source = excluded.source,
  metadata_json = excluded.metadata_json,
  updated_at = excluded.updated_at
`, item.ID, item.Text, item.Source, metadata, item.CreatedAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return refs, err
		}
		refs = append(refs, memory.MemoryRef{Provider: p.name, ID: item.ID})
	}
	if err := tx.Commit(); err != nil {
		return refs, err
	}
	committed = true
	return refs, nil
}

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := p.open(ctx)
	if err != nil {
		return err
	}
	return db.Close()
}

func (p *Provider) open(ctx context.Context) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", p.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := configure(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func configure(ctx context.Context, db *sql.DB) error {
	statements := []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memories (
  rowid INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  text TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  text,
  source,
  content='memories',
  content_rowid='rowid',
  tokenize='unicode61'
)`,
		`CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memory_fts(rowid, text, source) VALUES (new.rowid, new.text, new.source);
END`,
		`CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, text, source) VALUES('delete', old.rowid, old.text, old.source);
END`,
		`CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, text, source) VALUES('delete', old.rowid, old.text, old.source);
  INSERT INTO memory_fts(rowid, text, source) VALUES (new.rowid, new.text, new.source);
END`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) searchRecent(ctx context.Context, db *sql.DB, limit int) ([]memory.MemoryHit, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, text, source, metadata_json, created_at
FROM memories
ORDER BY created_at DESC
LIMIT ?
`, providerLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []memory.MemoryHit
	for rows.Next() {
		hit, err := scanHit(rows, p.name, nil)
		if err != nil {
			return nil, err
		}
		hit.Relevance = 0.1
		hit.Score = 0.1
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func (p *Provider) searchFTS(ctx context.Context, db *sql.DB, query memory.SearchQuery, terms []string) ([]memory.MemoryHit, error) {
	rows, err := db.QueryContext(ctx, `
SELECT m.id, m.text, m.source, m.metadata_json, m.created_at, bm25(memory_fts) AS rank
FROM memory_fts
JOIN memories AS m ON m.rowid = memory_fts.rowid
WHERE memory_fts MATCH ?
ORDER BY rank ASC, m.created_at DESC
LIMIT ?
`, ftsMatchQuery(terms), candidateLimit(query.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []memory.MemoryHit
	for rows.Next() {
		var rank sql.NullFloat64
		hit, err := scanHit(rows, p.name, &rank)
		if err != nil {
			return nil, err
		}
		if rank.Valid {
			rawScore := -rank.Float64
			hit.RawScore = &rawScore
			hit.RawScoreKind = "sqlite_fts_bm25_negated"
		}
		score := scoreMemory(terms, hit.Text)
		if score == 0 {
			continue
		}
		hit.Relevance = score
		hit.Score = score
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			if rawGreater(hits[i], hits[j]) {
				return true
			}
			if rawGreater(hits[j], hits[i]) {
				return false
			}
			return hits[i].CreatedAt.After(hits[j].CreatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
	if query.Limit > 0 && len(hits) > query.Limit {
		hits = hits[:query.Limit]
	}
	return hits, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHit(rows rowScanner, provider string, rank *sql.NullFloat64) (memory.MemoryHit, error) {
	var hit memory.MemoryHit
	var metadataJSON string
	var createdAt string
	dest := []any{&hit.ID, &hit.Text, &hit.Source, &metadataJSON, &createdAt}
	if rank != nil {
		dest = append(dest, rank)
	}
	if err := rows.Scan(dest...); err != nil {
		return memory.MemoryHit{}, err
	}
	metadata, err := decodeMetadata(metadataJSON)
	if err != nil {
		return memory.MemoryHit{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return memory.MemoryHit{}, fmt.Errorf("sqlite memory %q created_at: %w", hit.ID, err)
	}
	hit.Provider = provider
	hit.Metadata = metadata
	hit.CreatedAt = created
	return hit, nil
}

func encodeMetadata(metadata map[string]string) (string, error) {
	if metadata == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeMetadata(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" || value == "{}" {
		return nil, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(value), &metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func ftsMatchQuery(terms []string) string {
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
		field = strings.TrimSpace(field)
		if field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

func scoreMemory(queryTerms []string, text string) float64 {
	if len(queryTerms) == 0 {
		return 0.1
	}
	textTerms := normalizeTerms(text)
	if len(textTerms) == 0 {
		return 0
	}
	normalizedText := " " + strings.Join(textTerms, " ") + " "
	phrase := " " + strings.Join(queryTerms, " ") + " "
	if strings.Contains(normalizedText, phrase) {
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

func providerLimit(limit int) int {
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

func rawGreater(left, right memory.MemoryHit) bool {
	if left.RawScore == nil || right.RawScore == nil {
		return false
	}
	return *left.RawScore > *right.RawScore
}

func newID() string {
	var bytes [16]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(bytes[:])
}
