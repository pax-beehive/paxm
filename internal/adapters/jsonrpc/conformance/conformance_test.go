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
	dropStructuredAttribution       bool
}

func (p *providerStub) Name() string { return "stub" }
func (p *providerStub) Health(context.Context) error {
	if p.failHealth {
		return context.DeadlineExceeded
	}
	return nil
}
func (p *providerStub) Capabilities(context.Context) (jsonrpcadapter.Capabilities, error) {
	return jsonrpcadapter.Capabilities{PutBatch: true, Delete: true, Attribution: true}, nil
}
func (p *providerStub) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.item = memory.PrepareProviderItem(item)
	return memory.MemoryRef{ID: "one"}, nil
}

func (p *providerStub) PutBatch(_ context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	p.batch = map[string]memory.MemoryItem{"two": memory.PrepareProviderItem(items[0]), "three": memory.PrepareProviderItem(items[1])}
	return []memory.MemoryRef{{ID: "two"}, {ID: "three"}}, nil
}
func (p *providerStub) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	for id, item := range p.batch {
		if strings.Contains(item.Text, query.Text) {
			if p.dropStructuredAttribution {
				return []memory.MemoryHit{{ID: id, Text: item.Text, Metadata: item.Metadata}}, nil
			}
			return []memory.MemoryHit{{ID: id, Text: item.Text, Origin: item.Origin, Scope: item.Scope}}, nil
		}
	}
	if p.deleted {
		return nil, nil
	}
	if p.dropStructuredAttribution {
		return []memory.MemoryHit{{ID: "one", Text: p.item.Text, Metadata: p.item.Metadata}}, nil
	}
	return []memory.MemoryHit{{ID: "one", Text: p.item.Text, Metadata: p.item.Metadata, Origin: p.item.Origin, Scope: p.item.Scope}}, nil
}
func (p *providerStub) SearchWire(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	return p.Search(ctx, query)
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

func TestRunRejectsMetadataOnlyAttributionWhenCapabilityIsAdvertised(t *testing.T) {
	result := Run(context.Background(), &providerStub{dropStructuredAttribution: true})
	if result.Passed {
		t.Fatalf("result = %#v", result)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "attribution fidelity" && !check.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing failed attribution check: %#v", result.Checks)
	}
}
