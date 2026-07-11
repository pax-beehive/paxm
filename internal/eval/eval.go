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
	ID                  string            `json:"id"`
	Category            string            `json:"category"`
	Turns               []Turn            `json:"turns,omitempty"`
	Memories            []Memory          `json:"memories,omitempty"`
	Write               *Write            `json:"write,omitempty"`
	Recall              Recall            `json:"recall"`
	Expected            []string          `json:"expected,omitempty"`
	Forbidden           []string          `json:"forbidden,omitempty"`
	ExpectedFirst       string            `json:"expected_first,omitempty"`
	MaxHits             int               `json:"max_hits,omitempty"`
	ExpectedWrite       []string          `json:"expected_write,omitempty"`
	ForbiddenWrite      []string          `json:"forbidden_write,omitempty"`
	ForbiddenRecall     []string          `json:"forbidden_recall,omitempty"`
	ExpectedMetadata    map[string]string `json:"expected_metadata,omitempty"`
	RestartBeforeRecall bool              `json:"restart_before_recall,omitempty"`
	MaxMatchingHits     int               `json:"max_matching_hits,omitempty"`
	ExpectedEmpty       bool              `json:"expected_empty,omitempty"`
}

type Turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type Write struct {
	Target         string            `json:"target,omitempty"`
	Event          string            `json:"event"`
	Prompt         string            `json:"prompt,omitempty"`
	Assistant      string            `json:"assistant,omitempty"`
	IncludeHistory bool              `json:"include_history,omitempty"`
	Messages       []Turn            `json:"messages,omitempty"`
	Workspace      string            `json:"workspace,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Repeat         int               `json:"repeat,omitempty"`
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
	Suite                  string        `json:"suite"`
	Version                int           `json:"version"`
	CaseCount              int           `json:"case_count"`
	Passed                 int           `json:"passed"`
	Failed                 int           `json:"failed"`
	AdapterContractCases   int           `json:"adapter_contract_cases,omitempty"`
	AdapterContractPassed  int           `json:"adapter_contract_passed,omitempty"`
	AdapterContractFailed  int           `json:"adapter_contract_failed,omitempty"`
	RecallAtK              float64       `json:"recall_at_k"`
	PrecisionAtK           float64       `json:"precision_at_k"`
	MRR                    float64       `json:"mrr"`
	FalsePositiveRate      float64       `json:"false_positive_rate"`
	WriteCaseCount         int           `json:"write_case_count,omitempty"`
	Writes                 int           `json:"writes,omitempty"`
	WriteRecall            float64       `json:"write_recall,omitempty"`
	WritePrecision         float64       `json:"write_precision,omitempty"`
	WriteFalsePositiveRate float64       `json:"write_false_positive_rate,omitempty"`
	ResultCount            int           `json:"result_count,omitempty"`
	ReturnedContextBytes   int           `json:"returned_context_bytes,omitempty"`
	WriteDurationUS        int64         `json:"write_duration_us,omitempty"`
	RecallDurationUS       int64         `json:"recall_duration_us,omitempty"`
	DurationMS             int64         `json:"duration_ms"`
	Cases                  []CaseResult  `json:"cases"`
	Categories             []GroupResult `json:"categories"`
}

type CaseResult struct {
	ID                       string   `json:"id"`
	Category                 string   `json:"category"`
	Passed                   bool     `json:"passed"`
	AdapterContractCase      bool     `json:"adapter_contract_case,omitempty"`
	AdapterContractPassed    bool     `json:"adapter_contract_passed,omitempty"`
	AdapterContractErrors    []string `json:"adapter_contract_errors,omitempty"`
	HitIDs                   []string `json:"hit_ids"`
	Missing                  []string `json:"missing,omitempty"`
	Forbidden                []string `json:"forbidden_hits,omitempty"`
	Unexpected               []string `json:"unexpected_hits,omitempty"`
	RecallAtK                float64  `json:"recall_at_k"`
	PrecisionAtK             float64  `json:"precision_at_k"`
	ReciprocalRank           float64  `json:"reciprocal_rank"`
	Written                  bool     `json:"written,omitempty"`
	WriteCase                bool     `json:"-"`
	WriteMissing             []string `json:"write_missing,omitempty"`
	WriteForbidden           []string `json:"write_forbidden,omitempty"`
	WriteForbiddenCandidates int      `json:"-"`
	MetadataMismatches       []string `json:"metadata_mismatches,omitempty"`
	WriteRecall              float64  `json:"write_recall,omitempty"`
	WritePrecision           float64  `json:"write_precision,omitempty"`
	ResultCount              int      `json:"result_count,omitempty"`
	ReturnedContextBytes     int      `json:"returned_context_bytes,omitempty"`
	WriteDurationUS          int64    `json:"write_duration_us,omitempty"`
	RecallDurationUS         int64    `json:"recall_duration_us,omitempty"`
	DurationMS               int64    `json:"duration_ms"`
	Error                    string   `json:"error,omitempty"`
}

type GroupResult struct {
	Name                   string  `json:"name"`
	CaseCount              int     `json:"case_count"`
	Passed                 int     `json:"passed"`
	RecallAtK              float64 `json:"recall_at_k"`
	PrecisionAtK           float64 `json:"precision_at_k"`
	MRR                    float64 `json:"mrr"`
	WriteCaseCount         int     `json:"write_case_count,omitempty"`
	WriteRecall            float64 `json:"write_recall,omitempty"`
	WritePrecision         float64 `json:"write_precision,omitempty"`
	WriteFalsePositiveRate float64 `json:"write_false_positive_rate,omitempty"`
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
		if len(c.Memories) == 0 && c.Write == nil {
			return fmt.Errorf("case %q requires memories or a conversation write", c.ID)
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
		if c.Write == nil && len(c.Expected) == 0 && !c.ExpectedEmpty {
			return fmt.Errorf("case %q requires expected memory ids", c.ID)
		}
		if c.Write != nil {
			if strings.TrimSpace(c.Write.Event) == "" {
				return fmt.Errorf("case %q conversation write requires an event", c.ID)
			}
			if len(c.ExpectedWrite) == 0 {
				return fmt.Errorf("case %q conversation write requires expected_write", c.ID)
			}
			if err := validateTextAssertions(c.ID, c.ExpectedWrite, c.ForbiddenWrite, c.ForbiddenRecall); err != nil {
				return err
			}
			if c.Write.IncludeHistory && c.Write.Event != "turn_end" {
				return fmt.Errorf("case %q can include history only for turn_end", c.ID)
			}
			if c.Write.Repeat < 0 {
				return fmt.Errorf("case %q conversation write repeat must be positive", c.ID)
			}
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
		historyRoles := map[string]bool{}
		for _, turn := range c.Turns {
			if !validHistoryRole(turn.Role) || strings.TrimSpace(turn.Text) == "" {
				return fmt.Errorf("case %q has invalid normalized turn", c.ID)
			}
			historyRoles[strings.ToLower(strings.TrimSpace(turn.Role))] = true
		}
		if c.Write != nil {
			if !historyRoles["user"] || !historyRoles["assistant"] {
				return fmt.Errorf("case %q conversation write requires normalized user and assistant history", c.ID)
			}
			for _, message := range c.Write.Messages {
				if !validWriteMessageRole(message.Role) || strings.TrimSpace(message.Text) == "" {
					return fmt.Errorf("case %q has invalid hook write message", c.ID)
				}
			}
		}
	}
	return nil
}

func validateTextAssertions(caseID string, expectedWrite, forbiddenWrite, forbiddenRecall []string) error {
	seen := make(map[string]string, len(expectedWrite)+len(forbiddenWrite)+len(forbiddenRecall))
	for _, group := range []struct {
		name   string
		values []string
	}{
		{name: "expected_write", values: expectedWrite},
		{name: "forbidden_write", values: forbiddenWrite},
		{name: "forbidden_recall", values: forbiddenRecall},
	} {
		for _, value := range group.values {
			normalized := strings.ToLower(strings.TrimSpace(value))
			if normalized == "" {
				return fmt.Errorf("case %q %s contains a blank fragment", caseID, group.name)
			}
			if previous, ok := seen[normalized]; ok {
				return fmt.Errorf("case %q repeats write fragment %q in %s and %s", caseID, value, previous, group.name)
			}
			seen[normalized] = group.name
		}
	}
	return nil
}

func validHistoryRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant":
		return true
	default:
		return false
	}
}

func validWriteMessageRole(role string) bool {
	if validHistoryRole(role) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "tool_call", "tool_result", "reasoning", "analysis", "thinking":
		return true
	default:
		return false
	}
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
	result = CaseResult{
		ID:                       scenario.ID,
		Category:                 scenario.Category,
		WriteCase:                scenario.Write != nil,
		AdapterContractCase:      scenario.Write != nil,
		WriteForbiddenCandidates: len(scenario.ForbiddenWrite),
	}
	defer func() {
		if result.AdapterContractCase && !result.AdapterContractPassed && len(result.AdapterContractErrors) == 0 {
			failure := result.Error
			if failure == "" {
				failure = "adapter write contract did not complete"
			}
			result.AdapterContractErrors = []string{failure}
		}
		result.DurationMS = time.Since(started).Milliseconds()
	}()
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
	var writtenText string
	var writtenMetadata map[string]string
	if scenario.Write != nil {
		writeStarted := time.Now()
		messages := make([]facade.HookMessage, 0, len(scenario.Turns)+len(scenario.Write.Messages))
		if scenario.Write.IncludeHistory {
			for _, turn := range scenario.Turns {
				messages = append(messages, facade.HookMessage{Role: turn.Role, Text: turn.Text, Source: "eval-history:" + scenario.ID})
			}
		}
		for _, turn := range scenario.Write.Messages {
			messages = append(messages, facade.HookMessage{Role: turn.Role, Text: turn.Text, Source: "eval:" + scenario.ID})
		}
		item, ok, writeErr := runtime.Service.HookWriteItem(facade.HookEvent{
			Target:    defaultString(scenario.Write.Target, "codex"),
			Event:     scenario.Write.Event,
			Prompt:    scenario.Write.Prompt,
			Assistant: scenario.Write.Assistant,
			Messages:  messages,
			Workspace: scenario.Write.Workspace,
			Metadata:  scenario.Write.Metadata,
		})
		if writeErr != nil {
			result.Error = writeErr.Error()
			return result
		}
		if !ok {
			result.Error = "conversation write was skipped"
			return result
		}
		writtenText, writtenMetadata = item.Text, item.Metadata
		repeats := scenario.Write.Repeat
		if repeats == 0 {
			repeats = 1
		}
		var writeResult facade.IngestResult
		for range repeats {
			writeResult, writeErr = runtime.Service.IngestBatch(ctx, facade.IngestBatchInput{Items: []facade.IngestInput{item}})
			if writeErr != nil {
				break
			}
		}
		result.WriteDurationUS = time.Since(writeStarted).Microseconds()
		if writeErr != nil {
			result.Error = writeErr.Error()
			return result
		}
		result.Written = len(writeResult.Refs) > 0
		result.AdapterContractErrors = evaluateWriteContract(scenario, result.Written, writtenText, writtenMetadata)
		result.AdapterContractPassed = len(result.AdapterContractErrors) == 0
	}
	for _, item := range scenario.Memories {
		_, err = runtime.Service.Ingest(ctx, facade.IngestInput{ID: item.ID, Text: item.Text, Profile: profileForTier(item.Tier), Tier: item.Tier, ExpiresAt: item.ExpiresAt, Metadata: item.Metadata, Source: "eval:" + scenario.ID})
		if err != nil {
			result.Error = err.Error()
			return result
		}
	}
	if scenario.RestartBeforeRecall {
		runtime, err = paxruntime.Load(configPath)
		if err != nil {
			result.Error = err.Error()
			return result
		}
	}
	var hits []memory.MemoryHit
	recallStarted := time.Now()
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
	result.RecallDurationUS = time.Since(recallStarted).Microseconds()
	for _, hit := range hits {
		result.HitIDs = append(result.HitIDs, hit.ID)
		result.ReturnedContextBytes += len([]byte(hit.Text))
	}
	result.ResultCount = len(hits)
	if scenario.Write != nil {
		result.scoreConversationWrite(scenario, writtenText, writtenMetadata, hits)
	} else {
		result.score(scenario)
	}
	return result
}

func evaluateWriteContract(scenario Case, written bool, text string, metadata map[string]string) []string {
	var failures []string
	if !written {
		failures = append(failures, "provider did not acknowledge the write")
	}
	for _, expected := range scenario.ExpectedWrite {
		if !containsFold(text, expected) {
			failures = append(failures, "missing write fragment: "+expected)
		}
	}
	for _, forbidden := range scenario.ForbiddenWrite {
		if containsFold(text, forbidden) {
			failures = append(failures, "forbidden write fragment: "+forbidden)
		}
	}
	for key, expected := range scenario.ExpectedMetadata {
		if metadata[key] != expected {
			failures = append(failures, fmt.Sprintf("write metadata %s=%q; want %q", key, metadata[key], expected))
		}
	}
	sort.Strings(failures)
	return failures
}

func (r *CaseResult) scoreConversationWrite(scenario Case, writtenText string, metadata map[string]string, hits []memory.MemoryHit) {
	r.WriteForbiddenCandidates = len(scenario.ForbiddenWrite)
	expectedMatches := 0
	for _, expected := range scenario.ExpectedWrite {
		if containsFold(writtenText, expected) {
			expectedMatches++
		} else {
			r.WriteMissing = append(r.WriteMissing, expected)
		}
	}
	for _, forbidden := range scenario.ForbiddenWrite {
		if containsFold(writtenText, forbidden) {
			r.WriteForbidden = append(r.WriteForbidden, forbidden)
		}
	}
	for key, expected := range scenario.ExpectedMetadata {
		if metadata[key] != expected {
			r.MetadataMismatches = append(r.MetadataMismatches, fmt.Sprintf("write.%s=%q; want %q", key, metadata[key], expected))
		}
	}
	r.WriteRecall = float64(expectedMatches) / float64(len(scenario.ExpectedWrite))
	denominator := expectedMatches + len(r.WriteForbidden)
	if denominator > 0 {
		r.WritePrecision = float64(expectedMatches) / float64(denominator)
	}

	firstRank := 0
	matchingHits := 0
	recallMetadataOK := len(scenario.ExpectedMetadata) == 0
	var firstMatchingMetadata map[string]string
	for index, hit := range hits {
		matches := true
		for _, expected := range scenario.ExpectedWrite {
			if !containsFold(hit.Text, expected) {
				matches = false
				break
			}
		}
		if matches {
			matchingHits++
			if firstMatchingMetadata == nil {
				firstMatchingMetadata = hit.Metadata
			}
			if metadataContains(hit.Metadata, scenario.ExpectedMetadata) {
				recallMetadataOK = true
			}
			if firstRank == 0 {
				firstRank = index + 1
			}
		} else {
			r.Unexpected = append(r.Unexpected, hit.ID)
		}
		for _, forbidden := range scenario.ForbiddenRecall {
			if containsFold(hit.Text, forbidden) {
				r.Forbidden = append(r.Forbidden, hit.ID)
				break
			}
		}
	}
	if matchingHits > 0 && !recallMetadataOK {
		for key, expected := range scenario.ExpectedMetadata {
			if firstMatchingMetadata[key] != expected {
				r.MetadataMismatches = append(r.MetadataMismatches, fmt.Sprintf("recall.%s=%q; want %q", key, firstMatchingMetadata[key], expected))
			}
		}
	}
	sort.Strings(r.MetadataMismatches)
	if matchingHits > 0 {
		r.RecallAtK = 1
		r.PrecisionAtK = float64(matchingHits) / float64(len(hits))
		r.ReciprocalRank = 1 / float64(firstRank)
	}
	if scenario.MaxMatchingHits > 0 && matchingHits > scenario.MaxMatchingHits {
		r.Error = fmt.Sprintf("returned %d matching hits; expected at most %d", matchingHits, scenario.MaxMatchingHits)
	}
	r.Passed = r.Written && len(r.WriteMissing) == 0 && len(r.WriteForbidden) == 0 && len(r.Forbidden) == 0 && len(r.MetadataMismatches) == 0 && r.RecallAtK == 1 && r.Error == ""
}

func metadataContains(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func containsFold(text, fragment string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(fragment)))
}

func (r *CaseResult) score(scenario Case) {
	if scenario.ExpectedEmpty {
		r.Passed = len(r.HitIDs) == 0
		if !r.Passed {
			r.Unexpected = append(r.Unexpected, r.HitIDs...)
			r.Error = fmt.Sprintf("returned %d hits; expected none", len(r.HitIDs))
		}
		return
	}
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
		if c.AdapterContractCase {
			r.AdapterContractCases++
			if c.AdapterContractPassed {
				r.AdapterContractPassed++
			} else {
				r.AdapterContractFailed++
			}
		}
		r.RecallAtK += c.RecallAtK
		r.PrecisionAtK += c.PrecisionAtK
		r.MRR += c.ReciprocalRank
		r.FalsePositiveRate += float64(len(c.Unexpected))
		r.ResultCount += c.ResultCount
		r.ReturnedContextBytes += c.ReturnedContextBytes
		r.WriteDurationUS += c.WriteDurationUS
		r.RecallDurationUS += c.RecallDurationUS
		if c.WriteCase {
			r.WriteCaseCount++
			if c.Written {
				r.Writes++
			}
			r.WriteRecall += c.WriteRecall
			r.WritePrecision += c.WritePrecision
			r.WriteFalsePositiveRate += float64(len(c.WriteForbidden))
		}
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
	if r.WriteCaseCount > 0 {
		n := float64(r.WriteCaseCount)
		r.WriteRecall /= n
		r.WritePrecision /= n
		forbiddenCandidates := 0
		for _, c := range r.Cases {
			forbiddenCandidates += c.WriteForbiddenCandidates
		}
		if forbiddenCandidates > 0 {
			r.WriteFalsePositiveRate /= float64(forbiddenCandidates)
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		g := GroupResult{Name: name, CaseCount: len(groups[name])}
		groupForbiddenCandidates := 0
		for _, c := range groups[name] {
			if c.Passed {
				g.Passed++
			}
			g.RecallAtK += c.RecallAtK
			g.PrecisionAtK += c.PrecisionAtK
			g.MRR += c.ReciprocalRank
			if c.WriteCase {
				g.WriteCaseCount++
				g.WriteRecall += c.WriteRecall
				g.WritePrecision += c.WritePrecision
				g.WriteFalsePositiveRate += float64(len(c.WriteForbidden))
				groupForbiddenCandidates += c.WriteForbiddenCandidates
			}
		}
		n := float64(g.CaseCount)
		g.RecallAtK /= n
		g.PrecisionAtK /= n
		g.MRR /= n
		if g.WriteCaseCount > 0 {
			writeN := float64(g.WriteCaseCount)
			g.WriteRecall /= writeN
			g.WritePrecision /= writeN
			if groupForbiddenCandidates > 0 {
				g.WriteFalsePositiveRate /= float64(groupForbiddenCandidates)
			}
		}
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
