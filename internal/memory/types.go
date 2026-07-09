package memory

import (
	"context"
	"time"
)

type MemoryItem struct {
	ID        string            `json:"id,omitempty"`
	Text      string            `json:"text"`
	Source    string            `json:"source,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
}

type MemoryRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type SearchQuery struct {
	Text     string            `json:"text"`
	Limit    int               `json:"limit,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
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
}

type ProviderRoute struct {
	Name     string
	Required bool
	Weight   float64
}

type SearchPolicy struct {
	Providers    []ProviderRoute
	Limit        int
	MinRelevance float64
	MinScore     float64
	RecencyBoost float64
}

type PutPolicy struct {
	Providers []ProviderRoute
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

type ProviderError struct {
	Provider string `json:"provider"`
	Required bool   `json:"required"`
	Op       string `json:"op"`
	Error    string `json:"error"`
}

type ProviderHealth struct {
	Provider string `json:"provider"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}
