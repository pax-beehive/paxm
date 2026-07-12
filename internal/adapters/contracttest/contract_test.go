package contracttest

import (
	"context"
	"testing"

	"github.com/pax-beehive/paxm/internal/memory"
)

type fakeProvider struct{}

func (fakeProvider) Name() string                     { return "fake" }
func (fakeProvider) Health(ctx context.Context) error { return ctx.Err() }
func (fakeProvider) Put(ctx context.Context, _ memory.MemoryItem) (memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return memory.MemoryRef{}, err
	}
	return memory.MemoryRef{Provider: "fake", ID: "ref"}, nil
}
func (fakeProvider) Search(ctx context.Context, _ memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []memory.MemoryHit{{Provider: "fake", ID: "hit", Text: "text"}}, nil
}

func TestRunAcceptsConformingProvider(t *testing.T) {
	Run(t, fakeProvider{}, Expectation{Name: "fake", Item: memory.MemoryItem{Text: "text"}, Query: memory.SearchQuery{Text: "text"}, RefID: "ref", HitID: "hit", HitText: "text"})
}
