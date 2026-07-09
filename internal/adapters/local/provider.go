package local

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type Provider struct {
	name string
	path string
}

func New(name, path string) (*Provider, error) {
	if name == "" {
		return nil, errors.New("local provider name is required")
	}
	if path == "" {
		return nil, errors.New("local provider path is required")
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
	file, err := os.Open(p.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var hits []memory.MemoryHit
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var item memory.MemoryItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		score := scoreMemory(query.Text, item.Text)
		if query.Text != "" && score == 0 {
			continue
		}
		rawScore := score
		hits = append(hits, memory.MemoryHit{
			Provider:     p.name,
			ID:           item.ID,
			Text:         item.Text,
			Relevance:    score,
			Score:        score,
			RawScore:     &rawScore,
			RawScoreKind: "keyword_ratio",
			Source:       item.Source,
			Metadata:     item.Metadata,
			CreatedAt:    item.CreatedAt,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].CreatedAt.After(hits[j].CreatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
	if query.Limit > 0 && len(hits) > query.Limit {
		hits = hits[:query.Limit]
	}
	return hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	refs, err := p.PutBatch(ctx, []memory.MemoryItem{item})
	if err != nil {
		return memory.MemoryRef{}, err
	}
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("local provider did not store memory")
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
	for i := range items {
		if strings.TrimSpace(items[i].Text) == "" {
			return nil, errors.New("memory text is required")
		}
		if items[i].ID == "" {
			items[i].ID = newID()
		}
		if items[i].CreatedAt.IsZero() {
			items[i].CreatedAt = time.Now().UTC()
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(p.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	refs := make([]memory.MemoryRef, 0, len(items))
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return refs, err
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return refs, err
		}
		refs = append(refs, memory.MemoryRef{Provider: p.name, ID: item.ID})
	}
	return refs, nil
}

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(p.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func scoreMemory(query, text string) float64 {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return 0.1
	}
	text = strings.ToLower(text)
	if strings.Contains(text, query) {
		return 1
	}
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return 0.1
	}
	matched := 0
	for _, term := range terms {
		if strings.Contains(text, term) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(terms))
}

func newID() string {
	var bytes [16]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(bytes[:])
}
