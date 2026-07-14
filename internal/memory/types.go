package memory

import (
	"context"
	"strings"
	"time"
)

type MemoryTier string

const (
	TierSTM MemoryTier = "stm"
	TierLTM MemoryTier = "ltm"
)

func NormalizeTier(tier MemoryTier) MemoryTier {
	switch MemoryTier(strings.ToLower(strings.TrimSpace(string(tier)))) {
	case TierSTM:
		return TierSTM
	default:
		return TierLTM
	}
}

func NormalizeTiers(tiers []MemoryTier) []MemoryTier {
	if len(tiers) == 0 {
		return nil
	}
	seen := make(map[MemoryTier]struct{}, len(tiers))
	normalized := make([]MemoryTier, 0, len(tiers))
	for _, tier := range tiers {
		value := NormalizeTier(tier)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func EffectiveHitTier(hit MemoryHit) MemoryTier {
	if hit.Tier != "" {
		return NormalizeTier(hit.Tier)
	}
	if value := strings.TrimSpace(hit.Metadata["paxm_tier"]); value != "" {
		return NormalizeTier(MemoryTier(value))
	}
	return TierLTM
}

func EffectiveHitExpiresAt(hit MemoryHit) *time.Time {
	if hit.ExpiresAt != nil {
		return hit.ExpiresAt
	}
	for _, key := range []string{"paxm_expires_at", "expires_at"} {
		value := strings.TrimSpace(hit.Metadata[key])
		if value == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			parsed, err := time.Parse(layout, value)
			if err == nil {
				parsed = parsed.UTC()
				return &parsed
			}
		}
	}
	return nil
}

type MemoryItem struct {
	ID            string            `json:"id,omitempty"`
	Text          string            `json:"text"`
	AdmissionText string            `json:"-"`
	Source        string            `json:"source,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at,omitempty"`
	Tier          MemoryTier        `json:"tier,omitempty"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	Turn          *TurnContext      `json:"-"`
	Provenance    Provenance        `json:"provenance,omitempty"`
}

type Provenance struct {
	UserID    string `json:"user_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	ScopeType string `json:"scope_type,omitempty"`
	ScopeID   string `json:"scope_id,omitempty"`
}

// TurnContext carries capture boundaries to providers without exposing them as
// provider-neutral metadata. Providers that do not model turns can ignore it.
type TurnContext struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

type MemoryRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type SearchQuery struct {
	Text     string            `json:"text"`
	Limit    int               `json:"limit,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Tiers    []MemoryTier      `json:"tiers,omitempty"`
}

type MemoryHit struct {
	Provider     string            `json:"provider"`
	ID           string            `json:"id"`
	Text         string            `json:"text"`
	Relevance    float64           `json:"relevance"`
	Score        float64           `json:"score"`
	RawScore     *float64          `json:"raw_score,omitempty"`
	RawScoreKind string            `json:"raw_score_kind,omitempty"`
	Source       string            `json:"source,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at,omitempty"`
	Tier         MemoryTier        `json:"tier,omitempty"`
	ExpiresAt    *time.Time        `json:"expires_at,omitempty"`
	Provenance   Provenance        `json:"provenance,omitempty"`
	rankingScore float64
}

type ProviderRoute struct {
	Name         string
	Required     bool
	Weight       float64
	Timeout      time.Duration
	MinRelevance float64
	MinScore     float64
}

type SearchPolicy struct {
	Providers    []ProviderRoute
	Limit        int
	MinRelevance float64
	MinScore     float64
	RecencyBoost float64
	Tiers        []MemoryTier
}

type PutPolicy struct {
	Providers    []ProviderRoute
	Tier         MemoryTier
	ExpiresAfter time.Duration
}

type Provider interface {
	Name() string
	Search(ctx context.Context, query SearchQuery) ([]MemoryHit, error)
	Put(ctx context.Context, item MemoryItem) (MemoryRef, error)
	Health(ctx context.Context) error
}

type BatchProvider interface {
	PutBatch(ctx context.Context, items []MemoryItem) ([]MemoryRef, error)
}

// TurnBoundaryProvider opts into storing a complete turn as one memory item.
// Providers without this capability retain bounded backfill splitting.
type TurnBoundaryProvider interface {
	PreserveTurnBoundaries() bool
}

type CleanupExpiredProvider interface {
	CleanupExpired(ctx context.Context, limit int) (int, error)
}

type CloseProvider interface {
	Close() error
}

// DeleteProvider is an optional capability used when callers must remove
// specific writes, such as benchmark data recorded in an eval manifest.
type DeleteProvider interface {
	Delete(ctx context.Context, ref MemoryRef) error
}

// EvalScopeCleaner is an optional capability for providers that can remove an
// entire isolated benchmark scope more reliably than deleting individual refs.
type EvalScopeCleaner interface {
	CleanupEvalScope(ctx context.Context) error
}

type ProviderError struct {
	Provider string `json:"provider"`
	Required bool   `json:"required"`
	Op       string `json:"op"`
	Error    string `json:"error"`
}

const (
	ProviderRecallSuccess = "success"
	ProviderRecallError   = "error"
	ProviderRecallTimeout = "timeout"
)

type ProviderRecall struct {
	Provider     string `json:"provider"`
	DurationMS   int64  `json:"duration_ms"`
	Outcome      string `json:"outcome"`
	TimeoutMS    int64  `json:"timeout_ms,omitempty"`
	BulkheadBusy bool   `json:"bulkhead_busy,omitempty"`
}

type CleanupExpiredResult struct {
	Deleted        int             `json:"deleted"`
	ProviderErrors []ProviderError `json:"provider_errors,omitempty"`
}

type ProviderHealth struct {
	Provider string `json:"provider"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}
