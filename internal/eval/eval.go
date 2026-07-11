package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
	paxruntime "github.com/pax-beehive/memory-adaptor/internal/runtime"
)

const SuiteVersion = 1

type Suite struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Cases   []Case `json:"cases"`
}

type Case struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`
	Turns         []Turn   `json:"turns,omitempty"`
	Memories      []Memory `json:"memories"`
	Recall        Recall   `json:"recall"`
	Expected      []string `json:"expected"`
	Forbidden     []string `json:"forbidden,omitempty"`
	ExpectedFirst string   `json:"expected_first,omitempty"`
	MaxHits       int      `json:"max_hits,omitempty"`
}

type Turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type Memory struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Tier      memory.MemoryTier `json:"tier,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Recall struct {
	Mode      string            `json:"mode"`
	Query     string            `json:"query"`
	Profile   string            `json:"profile,omitempty"`
	Limit     int               `json:"limit,omitempty"`
	Target    string            `json:"target,omitempty"`
	Event     string            `json:"event,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Result struct {
	Suite             string        `json:"suite"`
	Version           int           `json:"version"`
	CaseCount         int           `json:"case_count"`
	Passed            int           `json:"passed"`
	Failed            int           `json:"failed"`
	RecallAtK         float64       `json:"recall_at_k"`
	PrecisionAtK      float64       `json:"precision_at_k"`
	MRR               float64       `json:"mrr"`
	FalsePositiveRate float64       `json:"false_positive_rate"`
	DurationMS        int64         `json:"duration_ms"`
	Cases             []CaseResult  `json:"cases"`
	Categories        []GroupResult `json:"categories"`
}

type CaseResult struct {
	ID             string   `json:"id"`
	Category       string   `json:"category"`
	Passed         bool     `json:"passed"`
	HitIDs         []string `json:"hit_ids"`
	Missing        []string `json:"missing,omitempty"`
	Forbidden      []string `json:"forbidden_hits,omitempty"`
	Unexpected     []string `json:"unexpected_hits,omitempty"`
	RecallAtK      float64  `json:"recall_at_k"`
	PrecisionAtK   float64  `json:"precision_at_k"`
	ReciprocalRank float64  `json:"reciprocal_rank"`
	DurationMS     int64    `json:"duration_ms"`
	Error          string   `json:"error,omitempty"`
}

type GroupResult struct {
	Name         string  `json:"name"`
	CaseCount    int     `json:"case_count"`
	Passed       int     `json:"passed"`
	RecallAtK    float64 `json:"recall_at_k"`
	PrecisionAtK float64 `json:"precision_at_k"`
	MRR          float64 `json:"mrr"`
}

func Load(path string) (Suite, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Suite{}, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "suite.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var suite Suite
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return Suite{}, fmt.Errorf("decode eval suite: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (s Suite) Validate() error {
	if s.Version != SuiteVersion {
		return fmt.Errorf("unsupported eval suite version %d; expected %d", s.Version, SuiteVersion)
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("eval suite name is required")
	}
	if len(s.Cases) == 0 {
		return errors.New("eval suite requires at least one case")
	}
	seenCases := map[string]bool{}
	for i, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("case %d id is required", i+1)
		}
		if seenCases[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seenCases[c.ID] = true
		if strings.TrimSpace(c.Category) == "" {
			return fmt.Errorf("case %q category is required", c.ID)
		}
		if len(c.Memories) == 0 {
			return fmt.Errorf("case %q requires memories", c.ID)
		}
		memoryIDs := map[string]bool{}
		for _, item := range c.Memories {
			if item.ID == "" || strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("case %q has memory without id or text", c.ID)
			}
			if memoryIDs[item.ID] {
				return fmt.Errorf("case %q has duplicate memory id %q", c.ID, item.ID)
			}
			memoryIDs[item.ID] = true
		}
		if strings.TrimSpace(c.Recall.Query) == "" {
			return fmt.Errorf("case %q recall query is required", c.ID)
		}
		if c.Recall.Mode != "active" && c.Recall.Mode != "passive" && c.Recall.Mode != "passive_initial" {
			return fmt.Errorf("case %q has unsupported recall mode %q", c.ID, c.Recall.Mode)
		}
		if len(c.Expected) == 0 {
			return fmt.Errorf("case %q requires expected memory ids", c.ID)
		}
		refs := append(append([]string{}, c.Expected...), c.Forbidden...)
		if c.ExpectedFirst != "" {
			refs = append(refs, c.ExpectedFirst)
		}
		for _, id := range refs {
			if !memoryIDs[id] {
				return fmt.Errorf("case %q references unknown memory %q", c.ID, id)
			}
		}
		for _, turn := range c.Turns {
			if (turn.Role != "user" && turn.Role != "assistant") || strings.TrimSpace(turn.Text) == "" {
				return fmt.Errorf("case %q has invalid normalized turn", c.ID)
			}
		}
	}
	return nil
}

type Runner struct{ Root string }

func (r Runner) Run(ctx context.Context, suite Suite) (Result, error) {
	if err := suite.Validate(); err != nil {
		return Result{}, err
	}
	root := r.Root
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "paxm-eval-")
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(root)
	}
	started := time.Now()
	result := Result{Suite: suite.Name, Version: suite.Version, CaseCount: len(suite.Cases)}
	for _, scenario := range suite.Cases {
		caseResult := runCase(ctx, root, scenario)
		result.Cases = append(result.Cases, caseResult)
		if caseResult.Passed {
			result.Passed++
		} else {
			result.Failed++
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.aggregate()
	return result, nil
}

func runCase(ctx context.Context, root string, scenario Case) (result CaseResult) {
	started := time.Now()
	result = CaseResult{ID: scenario.ID, Category: scenario.Category}
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	caseDir := filepath.Join(root, scenario.ID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		result.Error = err.Error()
		return result
	}
	configPath := filepath.Join(caseDir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	for name, agent := range cfg.Agents {
		agent.Enabled = true
		cfg.Agents[name] = agent
	}
	if err := config.Save(configPath, cfg); err != nil {
		result.Error = err.Error()
		return result
	}
	runtime, err := paxruntime.Load(configPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	for _, item := range scenario.Memories {
		_, err = runtime.Service.Ingest(ctx, facade.IngestInput{ID: item.ID, Text: item.Text, Profile: profileForTier(item.Tier), Tier: item.Tier, ExpiresAt: item.ExpiresAt, Metadata: item.Metadata, Source: "eval:" + scenario.ID})
		if err != nil {
			result.Error = err.Error()
			return result
		}
	}
	var hits []memory.MemoryHit
	if scenario.Recall.Mode == "active" {
		recalled, recallErr := runtime.Service.Recall(ctx, facade.RecallInput{Query: scenario.Recall.Query, Profile: scenario.Recall.Profile, Limit: scenario.Recall.Limit, Meta: scenario.Recall.Metadata})
		err, hits = recallErr, recalled.Hits
	} else {
		metadata := copyMap(scenario.Recall.Metadata)
		if scenario.Recall.Mode == "passive_initial" {
			metadata[facade.HookRecallPhaseMetadataKey] = facade.HookRecallPhaseInitial
		}
		hooked, hookErr := runtime.Service.RunHook(ctx, facade.HookEvent{Target: defaultString(scenario.Recall.Target, "codex"), Event: defaultString(scenario.Recall.Event, "user_input"), Prompt: scenario.Recall.Query, Query: scenario.Recall.Query, Limit: scenario.Recall.Limit, Workspace: scenario.Recall.Workspace, Metadata: metadata})
		err = hookErr
		if hooked.Recall != nil {
			hits = hooked.Recall.Hits
		}
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	for _, hit := range hits {
		result.HitIDs = append(result.HitIDs, hit.ID)
	}
	result.score(scenario)
	return result
}

func (r *CaseResult) score(scenario Case) {
	expected, forbidden := stringSet(scenario.Expected), stringSet(scenario.Forbidden)
	foundExpected := 0
	firstRank := 0
	for rank, id := range r.HitIDs {
		if expected[id] {
			foundExpected++
			if firstRank == 0 {
				firstRank = rank + 1
			}
		}
		if forbidden[id] {
			r.Forbidden = append(r.Forbidden, id)
		}
		if !expected[id] {
			r.Unexpected = append(r.Unexpected, id)
		}
	}
	for _, id := range scenario.Expected {
		if !contains(r.HitIDs, id) {
			r.Missing = append(r.Missing, id)
		}
	}
	r.RecallAtK = float64(foundExpected) / float64(len(scenario.Expected))
	if len(r.HitIDs) > 0 {
		r.PrecisionAtK = float64(foundExpected) / float64(len(r.HitIDs))
	}
	if firstRank > 0 {
		r.ReciprocalRank = 1 / float64(firstRank)
	}
	firstOK := scenario.ExpectedFirst == "" || (len(r.HitIDs) > 0 && r.HitIDs[0] == scenario.ExpectedFirst)
	limitOK := scenario.MaxHits <= 0 || len(r.HitIDs) <= scenario.MaxHits
	if !firstOK {
		r.Error = "expected first hit " + scenario.ExpectedFirst
	}
	if !limitOK {
		r.Error = fmt.Sprintf("returned %d hits; expected at most %d", len(r.HitIDs), scenario.MaxHits)
	}
	r.Passed = len(r.Missing) == 0 && len(r.Forbidden) == 0 && firstOK && limitOK && r.Error == ""
}

func (r *Result) aggregate() {
	groups := map[string][]CaseResult{}
	for _, c := range r.Cases {
		r.RecallAtK += c.RecallAtK
		r.PrecisionAtK += c.PrecisionAtK
		r.MRR += c.ReciprocalRank
		r.FalsePositiveRate += float64(len(c.Unexpected))
		groups[c.Category] = append(groups[c.Category], c)
	}
	if r.CaseCount > 0 {
		n := float64(r.CaseCount)
		r.RecallAtK /= n
		r.PrecisionAtK /= n
		r.MRR /= n
	}
	totalHits := 0
	for _, c := range r.Cases {
		totalHits += len(c.HitIDs)
	}
	if totalHits > 0 {
		r.FalsePositiveRate /= float64(totalHits)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		g := GroupResult{Name: name, CaseCount: len(groups[name])}
		for _, c := range groups[name] {
			if c.Passed {
				g.Passed++
			}
			g.RecallAtK += c.RecallAtK
			g.PrecisionAtK += c.PrecisionAtK
			g.MRR += c.ReciprocalRank
		}
		n := float64(g.CaseCount)
		g.RecallAtK /= n
		g.PrecisionAtK /= n
		g.MRR /= n
		r.Categories = append(r.Categories, g)
	}
}

func profileForTier(tier memory.MemoryTier) string {
	if memory.NormalizeTier(tier) == memory.TierSTM {
		return "stm"
	}
	return "ltm"
}
func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func copyMap(source map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range source {
		result[k] = v
	}
	return result
}
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
