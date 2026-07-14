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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/sqlite/retrieval"
	"github.com/pax-beehive/paxm/internal/memory"
	_ "modernc.org/sqlite"
)

type Provider struct {
	name   string
	path   string
	mu     sync.Mutex
	db     *sql.DB
	closed bool
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

func (*Provider) PreserveTurnBoundaries() bool { return true }

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := p.database(ctx)
	if err != nil {
		return nil, err
	}
	tiers := memory.NormalizeTiers(query.Tiers)
	request := retrieval.Request{
		Text: query.Text, Limit: query.Limit, Tiers: make([]string, len(tiers)),
		Workspace: strings.TrimSpace(query.Metadata["workspace"]),
	}
	for i, tier := range tiers {
		request.Tiers[i] = string(tier)
	}
	hits, err := retrieval.Search(ctx, db, request)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	result := make([]memory.MemoryHit, len(hits))
	for i, hit := range hits {
		result[i] = memory.MemoryHit{
			ID: hit.ID, Text: hit.Text, Source: hit.Source, Provider: p.name,
			Metadata: hit.Metadata, CreatedAt: hit.CreatedAt,
			Tier: memory.NormalizeTier(memory.MemoryTier(hit.Tier)), ExpiresAt: hit.ExpiresAt,
			Score: hit.Score, Relevance: hit.Score,
			RawScore: hit.RawScore, RawScoreKind: hit.RawScoreKind,
		}
		result[i] = memory.ApplyHitAttribution(result[i])
	}
	return result, nil
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
	db, err := p.database(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range items {
		items[i] = memory.PrepareProviderItem(items[i])
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
		itemMetadata, err := mergeLifecycleMetadata(ctx, tx, item)
		if err != nil {
			return refs, err
		}
		itemMetadata = sqliteTurnMetadata(itemMetadata, item.Turn)
		metadata, err := encodeMetadata(itemMetadata)
		if err != nil {
			return refs, err
		}
		tier := memory.NormalizeTier(item.Tier)
		expiresAt := ""
		if item.ExpiresAt != nil {
			expiresAt = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		_, err = tx.ExecContext(ctx, `
	INSERT INTO memories(id, text, source, metadata_json, created_at, updated_at, tier, expires_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
	  text = excluded.text,
	  source = excluded.source,
	  metadata_json = excluded.metadata_json,
	  updated_at = excluded.updated_at,
	  tier = excluded.tier,
	  expires_at = excluded.expires_at
	`, item.ID, item.Text, item.Source, metadata, item.CreatedAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), tier, expiresAt)
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

func sqliteTurnMetadata(metadata map[string]string, turn *memory.TurnContext) map[string]string {
	if turn == nil {
		return metadata
	}
	annotated := copyMetadata(metadata)
	annotated["session_id"] = strings.TrimSpace(turn.SessionID)
	annotated["turn_id"] = strings.TrimSpace(turn.TurnID)
	if !turn.StartedAt.IsZero() {
		annotated["started_at"] = turn.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !turn.EndedAt.IsZero() {
		annotated["ended_at"] = turn.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	return annotated
}

func mergeLifecycleMetadata(ctx context.Context, tx *sql.Tx, item memory.MemoryItem) (map[string]string, error) {
	incoming := copyMetadata(item.Metadata)
	fingerprint := incoming[memory.MetadataFingerprint]
	if strings.TrimSpace(fingerprint) == "" {
		return incoming, nil
	}

	var existingJSON string
	var existingCreatedAt string
	err := tx.QueryRowContext(ctx, `
	SELECT metadata_json, created_at
	FROM memories
	WHERE id = ?
	`, item.ID).Scan(&existingJSON, &existingCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return incoming, nil
	}
	if err != nil {
		return nil, err
	}
	existing, err := decodeMetadata(existingJSON)
	if err != nil {
		return nil, err
	}
	if existingFingerprint := strings.TrimSpace(existing[memory.MetadataFingerprint]); existingFingerprint != fingerprint {
		return nil, fmt.Errorf("sqlite memory %q fingerprint conflict", item.ID)
	}

	merged := copyMetadata(existing)
	for key, value := range incoming {
		merged[key] = value
	}
	merged[memory.MetadataFingerprint] = fingerprint
	merged[memory.MetadataOccurrences] = strconv.Itoa(metadataOccurrences(existing) + metadataOccurrences(incoming))
	merged[memory.MetadataFirstSeenAt] = earlierTimestamp(
		firstNonEmpty(existing[memory.MetadataFirstSeenAt], existingCreatedAt),
		incoming[memory.MetadataFirstSeenAt],
	)
	merged[memory.MetadataLastSeenAt] = laterTimestamp(
		firstNonEmpty(existing[memory.MetadataLastSeenAt], existingCreatedAt),
		firstNonEmpty(incoming[memory.MetadataLastSeenAt], item.CreatedAt.UTC().Format(time.RFC3339Nano)),
	)
	return merged, nil
}

func copyMetadata(metadata map[string]string) map[string]string {
	copied := make(map[string]string, len(metadata)+4)
	for key, value := range metadata {
		copied[key] = value
	}
	return copied
}

func metadataOccurrences(metadata map[string]string) int {
	value, err := strconv.Atoi(strings.TrimSpace(metadata[memory.MetadataOccurrences]))
	if err != nil || value < 1 {
		return 1
	}
	return value
}

func earlierTimestamp(left, right string) string {
	leftTime, leftOK := parseTimestamp(left)
	rightTime, rightOK := parseTimestamp(right)
	if !leftOK {
		return right
	}
	if !rightOK || leftTime.Before(rightTime) {
		return left
	}
	return right
}

func laterTimestamp(left, right string) string {
	leftTime, leftOK := parseTimestamp(left)
	rightTime, rightOK := parseTimestamp(right)
	if !leftOK {
		return right
	}
	if !rightOK || leftTime.After(rightTime) {
		return left
	}
	return right
}

func parseTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed, err == nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := p.database(ctx)
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func (p *Provider) CleanupExpired(ctx context.Context, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	db, err := p.database(ctx)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `
	DELETE FROM memories
	WHERE rowid IN (
	  SELECT rowid
	  FROM memories
	  WHERE expires_at != '' AND expires_at <= ?
	  ORDER BY expires_at ASC
	  LIMIT ?
	)
	`, time.Now().UTC().Format(time.RFC3339Nano), cleanupLimit(limit))
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(deleted), nil
}

func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db == nil {
		p.closed = true
		return nil
	}
	err := p.db.Close()
	p.db = nil
	p.closed = true
	return err
}

func (p *Provider) database(ctx context.Context) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("sqlite provider is closed")
	}
	if p.db != nil {
		return p.db, nil
	}
	db, err := p.open(ctx)
	if err != nil {
		return nil, err
	}
	p.db = db
	return db, nil
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
	  updated_at TEXT NOT NULL,
	  tier TEXT NOT NULL DEFAULT 'ltm',
	  expires_at TEXT NOT NULL DEFAULT ''
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
	if err := ensureColumn(ctx, db, "tier", "TEXT NOT NULL DEFAULT 'ltm'"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "expires_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, name, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(memories)")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if columnName == name {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE memories ADD COLUMN "+name+" "+definition)
	return err
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

func cleanupLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 500
	}
	return limit
}

func newID() string {
	var bytes [16]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(bytes[:])
}
