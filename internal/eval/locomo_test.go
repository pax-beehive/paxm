package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type locomoTestProvider struct {
	items   []memory.MemoryItem
	deleted []string
}

func (p *locomoTestProvider) Name() string { return "primary" }
func (p *locomoTestProvider) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.items = append(p.items, item)
	return memory.MemoryRef{Provider: p.Name(), ID: item.ID}, nil
}
func (p *locomoTestProvider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	var refs []memory.MemoryRef
	for _, item := range items {
		ref, err := p.Put(ctx, item)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
func (p *locomoTestProvider) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	for _, item := range p.items {
		if item.ID == "D2:1" {
			return []memory.MemoryHit{{ID: item.ID, Text: item.Text, Metadata: item.Metadata, Relevance: 1, Score: 1}}, nil
		}
	}
	return nil, nil
}
func (p *locomoTestProvider) Health(context.Context) error { return nil }
func (p *locomoTestProvider) Delete(_ context.Context, ref memory.MemoryRef) error {
	p.deleted = append(p.deleted, ref.ID)
	return nil
}

func TestLoadLoCoMoParsesOrderedSessionsAndTextQA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locomo.json")
	data := `[
  {
    "sample_id": "sample-1",
    "qa": [
      {"question":"What did Alice adopt?","answer":"A dog","evidence":["D2:1"],"category":1},
      {"question":"Adversarial?","evidence":["D1:1"],"category":5,"adversarial_answer":"wrong"}
    ],
    "conversation": {
      "speaker_a":"Alice",
      "speaker_b":"Bob",
      "session_2_date_time":"2 June 2023",
      "session_2":[{"speaker":"Alice","dia_id":"D2:1","text":"I adopted a dog."}],
      "session_1_date_time":"1 June 2023",
      "session_1":[{"speaker":"Bob","dia_id":"D1:1","text":"Hello Alice."}]
    }
  }
]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	dataset, err := LoadLoCoMo(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Conversations) != 1 {
		t.Fatalf("conversation count = %d", len(dataset.Conversations))
	}
	conversation := dataset.Conversations[0]
	if len(conversation.Sessions) != 2 || conversation.Sessions[0].Number != 1 || conversation.Sessions[1].Number != 2 {
		t.Fatalf("sessions = %#v", conversation.Sessions)
	}
	if len(conversation.Questions) != 1 {
		t.Fatalf("text QA count = %d, want category 5 excluded", len(conversation.Questions))
	}
	if conversation.Questions[0].Evidence[0] != "D2:1" {
		t.Fatalf("question = %#v", conversation.Questions[0])
	}
}

func TestLoCoMoRunnerIngestsScoresAndCleansEachConversation(t *testing.T) {
	dataset := LoCoMoDataset{Conversations: []LoCoMoConversation{{
		ID: "sample-1",
		Sessions: []LoCoMoSession{{Number: 1, DateTime: "1 June 2023", Turns: []LoCoMoTurn{
			{Speaker: "Alice", ID: "D2:1", Text: "I adopted a dog."},
			{Speaker: "Bob", ID: "D2:2", Text: "That is great."},
		}}},
		Questions: []LoCoMoQuestion{{Question: "What did Alice adopt?", Evidence: []string{"D2:1"}, Category: 1}},
	}}}
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["primary"] = config.ProviderConfig{Type: "mem0", Enabled: true, RunID: "normal"}
	provider := &locomoTestProvider{}
	runner := LoCoMoRunner{BuildProvider: func(string, config.ProviderConfig) (memory.Provider, error) { return provider, nil }}
	result, err := runner.Run(context.Background(), dataset, LoCoMoRunOptions{
		Config: cfg, Provider: "primary", RunID: "locomo-test", ManifestDir: dir, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.QuestionCount != 1 || result.RecallAtK != 1 || result.PrecisionAtK != 0.2 || result.MRR != 1 || result.Passed != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.items) != 2 || provider.items[0].Metadata["locomo_dia_id"] != "D2:1" {
		t.Fatalf("ingested items = %#v", provider.items)
	}
	if len(provider.deleted) != 2 {
		t.Fatalf("deleted refs = %#v", provider.deleted)
	}
	manifest, err := LoadEvalManifest(filepath.Join(dir, "locomo-test-sample-1", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != EvalStatusCleaned {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestLoCoMoRunnerCountsProviderFailureForEveryQuestion(t *testing.T) {
	dataset := LoCoMoDataset{Conversations: []LoCoMoConversation{{
		ID:       "sample-1",
		Sessions: []LoCoMoSession{{Number: 1, Turns: []LoCoMoTurn{{ID: "D1:1", Text: "memory"}}}},
		Questions: []LoCoMoQuestion{
			{Question: "one?", Evidence: []string{"D1:1"}, Category: 1},
			{Question: "two?", Evidence: []string{"D1:1"}, Category: 2},
		},
	}}}
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["unsafe"] = config.ProviderConfig{Type: "mem0", Enabled: true, RunID: "normal"}
	unsafe := &providerWithoutCleanup{}
	runner := LoCoMoRunner{BuildProvider: func(string, config.ProviderConfig) (memory.Provider, error) { return unsafe, nil }}
	result, err := runner.Run(context.Background(), dataset, LoCoMoRunOptions{Config: cfg, Provider: "unsafe", RunID: "fail", ManifestDir: dir})
	if err == nil {
		t.Fatal("expected cleanup capability error")
	}
	if result.QuestionCount != 2 || result.ExecutionFailed != 2 || result.Failed != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(unsafe.items) != 0 {
		t.Fatal("unsafe provider received writes before fail-closed check")
	}
}

func TestScoreLoCoMoQuestionIgnoresEvidenceBeyondK(t *testing.T) {
	question := LoCoMoQuestion{Question: "target?", Evidence: []string{"D3"}, Category: 1}
	hits := []memory.MemoryHit{{ID: "D1"}, {ID: "D2"}, {ID: "D3"}}
	result := scoreLoCoMoQuestion("conversation", question, hits, 2)
	if result.Passed || result.RecallAtK != 0 || result.ReciprocalRank != 0 || len(result.HitIDs) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

type providerWithoutCleanup struct{ items []memory.MemoryItem }

func (p *providerWithoutCleanup) Name() string { return "unsafe" }
func (p *providerWithoutCleanup) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return nil, nil
}
func (p *providerWithoutCleanup) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.items = append(p.items, item)
	return memory.MemoryRef{Provider: p.Name(), ID: item.ID}, nil
}
func (p *providerWithoutCleanup) Health(context.Context) error { return nil }
