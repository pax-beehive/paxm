package contracttest

import (
	"context"
	"errors"
	"testing"

	"github.com/pax-beehive/paxm/internal/memory"
)

type Expectation struct {
	Name         string
	Item         memory.MemoryItem
	Query        memory.SearchQuery
	RefID        string
	HitID        string
	HitText      string
	AssertPut    func(*testing.T)
	AssertSearch func(*testing.T)
}

func Run(t *testing.T, provider memory.Provider, expected Expectation) {
	t.Helper()
	ctx := context.Background()
	if provider.Name() != expected.Name {
		t.Fatalf("provider name = %q, want %q", provider.Name(), expected.Name)
	}
	if err := provider.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	ref, err := provider.Put(ctx, expected.Item)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Provider != expected.Name || ref.ID == "" || (expected.RefID != "" && ref.ID != expected.RefID) {
		t.Fatalf("put ref = %#v", ref)
	}
	if expected.AssertPut != nil {
		expected.AssertPut(t)
	}
	hits, err := provider.Search(ctx, expected.Query)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one", hits)
	}
	if hits[0].Provider != expected.Name || hits[0].ID == "" || (expected.HitID != "" && hits[0].ID != expected.HitID) || hits[0].Text != expected.HitText {
		t.Fatalf("mapped hit = %#v", hits[0])
	}
	if expected.AssertSearch != nil {
		expected.AssertSearch(t)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for operation, err := range map[string]error{
		"health": provider.Health(canceled),
		"put":    func() error { _, err := provider.Put(canceled, expected.Item); return err }(),
		"search": func() error { _, err := provider.Search(canceled, expected.Query); return err }(),
	} {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation error = %v, want context.Canceled", operation, err)
		}
	}
}
