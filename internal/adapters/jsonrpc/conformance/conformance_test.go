package conformance

import (
	"context"
	"strings"
	"testing"

	jsonrpcadapter "github.com/pax-beehive/paxm/internal/adapters/jsonrpc"
	"github.com/pax-beehive/paxm/internal/memory"
)

type providerStub struct {
	item                            memory.MemoryItem
	batch                           map[string]memory.MemoryItem
	deleted, failHealth, failDelete bool
}

func (p *providerStub) Name() string { return "stub" }
func (p *providerStub) Health(context.Context) error {
	if p.failHealth {
		return context.DeadlineExceeded
	}
	return nil
}
func (p *providerStub) Capabilities(context.Context) (jsonrpcadapter.Capabilities, error) {
	return jsonrpcadapter.Capabilities{PutBatch: true, Delete: true}, nil
}
func (p *providerStub) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.item = item
	return memory.MemoryRef{ID: "one"}, nil
}

func (p *providerStub) PutBatch(_ context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	p.batch = map[string]memory.MemoryItem{"two": items[0], "three": items[1]}
	return []memory.MemoryRef{{ID: "two"}, {ID: "three"}}, nil
}
func (p *providerStub) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	for id, item := range p.batch {
		if strings.Contains(item.Text, query.Text) {
			return []memory.MemoryHit{{ID: id, Text: item.Text}}, nil
		}
	}
	if p.deleted {
		return nil, nil
	}
	return []memory.MemoryHit{{ID: "one", Text: p.item.Text, Metadata: p.item.Metadata}}, nil
}
func (p *providerStub) Delete(_ context.Context, _ memory.MemoryRef) error {
	if p.failDelete {
		return context.DeadlineExceeded
	}
	p.deleted = true
	return nil
}

func TestRunPassesRequiredAndAdvertisedLifecycleChecks(t *testing.T) {
	result := Run(context.Background(), &providerStub{})
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	for _, check := range result.Checks {
		if !check.Skipped && !check.Passed {
			t.Fatalf("check = %#v", check)
		}
	}
}
func TestRunFailsRequiredHealthCheck(t *testing.T) {
	result := Run(context.Background(), &providerStub{failHealth: true})
	if result.Passed {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunFailsAdvertisedLifecycleCapability(t *testing.T) {
	result := Run(context.Background(), &providerStub{failDelete: true})
	if result.Passed {
		t.Fatalf("result = %#v", result)
	}
}
