package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type LoCoMoDataset struct {
	Conversations []LoCoMoConversation
}

type LoCoMoConversation struct {
	ID        string
	SpeakerA  string
	SpeakerB  string
	Sessions  []LoCoMoSession
	Questions []LoCoMoQuestion
}

type LoCoMoSession struct {
	Number   int
	DateTime string
	Turns    []LoCoMoTurn
}

type LoCoMoTurn struct {
	Speaker string `json:"speaker"`
	ID      string `json:"dia_id"`
	Text    string `json:"text"`
	Caption string `json:"blip_caption,omitempty"`
}

type LoCoMoQuestion struct {
	Question string          `json:"question"`
	Answer   json.RawMessage `json:"answer"`
	Evidence []string        `json:"evidence"`
	Category int             `json:"category"`
}

type rawLoCoMoConversation struct {
	SampleID     string                     `json:"sample_id"`
	QA           []LoCoMoQuestion           `json:"qa"`
	Conversation map[string]json.RawMessage `json:"conversation"`
}

type LoCoMoRunOptions struct {
	Config      config.Config
	Provider    string
	RunID       string
	ManifestDir string
	Limit       int
	KeepMemory  bool
	Settle      time.Duration
}

type LoCoMoRunner struct {
	BuildProvider func(string, config.ProviderConfig) (memory.Provider, error)
}

type LoCoMoResult struct {
	Benchmark         string                 `json:"benchmark"`
	DatasetVersion    string                 `json:"dataset_version"`
	Provider          string                 `json:"provider"`
	ConversationCount int                    `json:"conversation_count"`
	QuestionCount     int                    `json:"question_count"`
	Passed            int                    `json:"passed"`
	Failed            int                    `json:"failed"`
	ExecutionFailed   int                    `json:"execution_failed"`
	RecallAtK         float64                `json:"recall_at_k"`
	PrecisionAtK      float64                `json:"precision_at_k"`
	MRR               float64                `json:"mrr"`
	DurationMS        int64                  `json:"duration_ms"`
	Questions         []LoCoMoQuestionResult `json:"questions"`
	Categories        []LoCoMoCategoryResult `json:"categories"`
}

type LoCoMoQuestionResult struct {
	ConversationID string   `json:"conversation_id"`
	Question       string   `json:"question"`
	Category       int      `json:"category"`
	Evidence       []string `json:"evidence"`
	HitIDs         []string `json:"hit_ids"`
	Matched        []string `json:"matched_evidence,omitempty"`
	RecallAtK      float64  `json:"recall_at_k"`
	PrecisionAtK   float64  `json:"precision_at_k"`
	ReciprocalRank float64  `json:"reciprocal_rank"`
	Passed         bool     `json:"passed"`
	Error          string   `json:"error,omitempty"`
}

type LoCoMoCategoryResult struct {
	Category     int     `json:"category"`
	Questions    int     `json:"questions"`
	Passed       int     `json:"passed"`
	RecallAtK    float64 `json:"recall_at_k"`
	PrecisionAtK float64 `json:"precision_at_k"`
	MRR          float64 `json:"mrr"`
}

func (r LoCoMoRunner) Run(ctx context.Context, dataset LoCoMoDataset, opts LoCoMoRunOptions) (result LoCoMoResult, err error) {
	if r.BuildProvider == nil {
		return result, errors.New("LoCoMo provider factory is required")
	}
	if len(dataset.Conversations) == 0 {
		return result, errors.New("LoCoMo dataset is empty")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	started := time.Now()
	result = LoCoMoResult{Benchmark: "locomo-text-qa-retrieval", DatasetVersion: "official-locomo10", Provider: opts.Provider, ConversationCount: len(dataset.Conversations)}
	for _, conversation := range dataset.Conversations {
		conversationResult, runErr := r.runConversation(ctx, conversation, opts)
		if runErr != nil && len(conversationResult) < len(conversation.Questions) {
			conversationResult = failedLoCoMoQuestions(conversation, runErr)
		}
		result.Questions = append(result.Questions, conversationResult...)
		if runErr != nil {
			err = errors.Join(err, runErr)
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.aggregate()
	return result, err
}

func (r LoCoMoRunner) runConversation(ctx context.Context, conversation LoCoMoConversation, opts LoCoMoRunOptions) (results []LoCoMoQuestionResult, err error) {
	runID := sanitizeScopeID(opts.RunID + "-" + conversation.ID)
	scope, err := PrepareProviderScope(opts.Config, opts.Provider, ScopeOptions{RunID: runID, ManifestDir: opts.ManifestDir, KeepMemory: opts.KeepMemory})
	if err != nil {
		return nil, err
	}
	providerConfig := scope.Config.Providers[opts.Provider]
	provider, err := r.BuildProvider(opts.Provider, providerConfig)
	if err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return nil, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupErr := CleanupProviderScope(cleanupCtx, scope, provider)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if err := validateEvalCleanupCapability(scope, provider); err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return nil, err
	}

	err = ingestLoCoMoItems(ctx, provider, loCoMoMemoryItems(conversation), scope)
	if err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return nil, err
	}
	if opts.Settle > 0 {
		timer := time.NewTimer(opts.Settle)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	for _, question := range conversation.Questions {
		hits, searchErr := provider.Search(ctx, memory.SearchQuery{
			Text: question.Question, Limit: opts.Limit,
			Metadata: map[string]string{"locomo_conversation_id": conversation.ID},
		})
		questionResult := scoreLoCoMoQuestion(conversation.ID, question, hits, opts.Limit)
		if searchErr != nil {
			questionResult.Error = searchErr.Error()
			questionResult.Passed = false
		}
		results = append(results, questionResult)
	}
	if err := scope.SetStatus(EvalStatusComplete, nil); err != nil {
		return results, err
	}
	return results, nil
}

func validateEvalCleanupCapability(scope *ProviderScope, provider memory.Provider) error {
	if scope.Manifest.KeepMemory || scope.Manifest.ProviderType == "sqlite" {
		return nil
	}
	if _, ok := provider.(memory.EvalScopeCleaner); ok {
		return nil
	}
	if _, ok := provider.(memory.DeleteProvider); ok {
		return nil
	}
	return fmt.Errorf("provider %q does not implement reliable eval cleanup", scope.Manifest.Provider)
}

func failedLoCoMoQuestions(conversation LoCoMoConversation, runErr error) []LoCoMoQuestionResult {
	results := make([]LoCoMoQuestionResult, 0, len(conversation.Questions))
	for _, question := range conversation.Questions {
		results = append(results, LoCoMoQuestionResult{
			ConversationID: conversation.ID,
			Question:       question.Question,
			Category:       question.Category,
			Evidence:       append([]string(nil), question.Evidence...),
			Error:          runErr.Error(),
		})
	}
	return results
}

func ingestLoCoMoItems(ctx context.Context, provider memory.Provider, items []memory.MemoryItem, scope *ProviderScope) error {
	if batch, ok := provider.(memory.BatchProvider); ok {
		const batchSize = 50
		for start := 0; start < len(items); start += batchSize {
			end := min(start+batchSize, len(items))
			refs, err := batch.PutBatch(ctx, items[start:end])
			if recordErr := scope.RecordRefs(refs); recordErr != nil {
				return errors.Join(err, recordErr)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	for _, item := range items {
		ref, err := provider.Put(ctx, item)
		if ref.ID != "" {
			if recordErr := scope.RecordRefs([]memory.MemoryRef{ref}); recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func loCoMoMemoryItems(conversation LoCoMoConversation) []memory.MemoryItem {
	var items []memory.MemoryItem
	for _, session := range conversation.Sessions {
		for _, turn := range session.Turns {
			text := fmt.Sprintf("[%s] %s: %s", session.DateTime, turn.Speaker, strings.TrimSpace(turn.Text))
			if strings.TrimSpace(turn.Caption) != "" {
				text += " [Image: " + strings.TrimSpace(turn.Caption) + "]"
			}
			items = append(items, memory.MemoryItem{
				ID: turn.ID, Text: text, Source: "locomo", Tier: memory.TierLTM,
				Metadata: map[string]string{
					"benchmark": "locomo", "locomo_conversation_id": conversation.ID,
					"locomo_session": strconv.Itoa(session.Number), "locomo_dia_id": turn.ID,
				},
			})
		}
	}
	return items
}

func scoreLoCoMoQuestion(conversationID string, question LoCoMoQuestion, hits []memory.MemoryHit, limit int) LoCoMoQuestionResult {
	result := LoCoMoQuestionResult{ConversationID: conversationID, Question: question.Question, Category: question.Category, Evidence: append([]string(nil), question.Evidence...)}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	expected := make(map[string]bool, len(question.Evidence))
	for _, id := range question.Evidence {
		expected[id] = true
	}
	matched := make(map[string]bool)
	for index, hit := range hits {
		id := loCoMoHitID(hit)
		result.HitIDs = append(result.HitIDs, id)
		if expected[id] && !matched[id] {
			matched[id] = true
			result.Matched = append(result.Matched, id)
			if result.ReciprocalRank == 0 {
				result.ReciprocalRank = 1 / float64(index+1)
			}
		}
	}
	if len(expected) > 0 {
		result.RecallAtK = float64(len(matched)) / float64(len(expected))
	}
	if limit > 0 {
		result.PrecisionAtK = float64(len(matched)) / float64(limit)
	}
	result.Passed = len(matched) == len(expected)
	sort.Strings(result.Matched)
	return result
}

func loCoMoHitID(hit memory.MemoryHit) string {
	for _, key := range []string{"locomo_dia_id", "paxm_id"} {
		if value := strings.TrimSpace(hit.Metadata[key]); value != "" {
			return value
		}
	}
	return hit.ID
}

func (r *LoCoMoResult) aggregate() {
	groups := make(map[int]*LoCoMoCategoryResult)
	for _, question := range r.Questions {
		r.QuestionCount++
		if question.Error != "" {
			r.ExecutionFailed++
		}
		if question.Passed {
			r.Passed++
		} else {
			r.Failed++
		}
		r.RecallAtK += question.RecallAtK
		r.PrecisionAtK += question.PrecisionAtK
		r.MRR += question.ReciprocalRank
		group := groups[question.Category]
		if group == nil {
			group = &LoCoMoCategoryResult{Category: question.Category}
			groups[question.Category] = group
		}
		group.Questions++
		if question.Passed {
			group.Passed++
		}
		group.RecallAtK += question.RecallAtK
		group.PrecisionAtK += question.PrecisionAtK
		group.MRR += question.ReciprocalRank
	}
	if r.QuestionCount > 0 {
		r.RecallAtK /= float64(r.QuestionCount)
		r.PrecisionAtK /= float64(r.QuestionCount)
		r.MRR /= float64(r.QuestionCount)
	}
	for category := 1; category <= 4; category++ {
		group := groups[category]
		if group == nil {
			continue
		}
		group.RecallAtK /= float64(group.Questions)
		group.PrecisionAtK /= float64(group.Questions)
		group.MRR /= float64(group.Questions)
		r.Categories = append(r.Categories, *group)
	}
}

func LoadLoCoMo(path string) (LoCoMoDataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LoCoMoDataset{}, err
	}
	var raw []rawLoCoMoConversation
	if err := json.Unmarshal(data, &raw); err != nil {
		return LoCoMoDataset{}, fmt.Errorf("decode LoCoMo dataset: %w", err)
	}
	if len(raw) == 0 {
		return LoCoMoDataset{}, errors.New("LoCoMo dataset is empty")
	}
	dataset := LoCoMoDataset{Conversations: make([]LoCoMoConversation, 0, len(raw))}
	for i, item := range raw {
		conversation, err := parseLoCoMoConversation(item, i)
		if err != nil {
			return LoCoMoDataset{}, err
		}
		dataset.Conversations = append(dataset.Conversations, conversation)
	}
	return dataset, nil
}

func parseLoCoMoConversation(raw rawLoCoMoConversation, index int) (LoCoMoConversation, error) {
	id := strings.TrimSpace(raw.SampleID)
	if id == "" {
		id = fmt.Sprintf("conversation-%d", index+1)
	}
	conversation := LoCoMoConversation{ID: id}
	for key, target := range map[string]*string{"speaker_a": &conversation.SpeakerA, "speaker_b": &conversation.SpeakerB} {
		value, ok := raw.Conversation[key]
		if !ok {
			return LoCoMoConversation{}, fmt.Errorf("LoCoMo %s is missing %s", id, key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return LoCoMoConversation{}, fmt.Errorf("decode LoCoMo %s %s: %w", id, key, err)
		}
		if strings.TrimSpace(*target) == "" {
			return LoCoMoConversation{}, fmt.Errorf("LoCoMo %s has empty %s", id, key)
		}
	}
	for key, value := range raw.Conversation {
		number, ok := sessionNumber(key)
		if !ok {
			continue
		}
		var turns []LoCoMoTurn
		if err := json.Unmarshal(value, &turns); err != nil {
			return LoCoMoConversation{}, fmt.Errorf("decode LoCoMo %s %s: %w", id, key, err)
		}
		var dateTime string
		dateKey := fmt.Sprintf("session_%d_date_time", number)
		if dateValue, exists := raw.Conversation[dateKey]; exists {
			if err := json.Unmarshal(dateValue, &dateTime); err != nil {
				return LoCoMoConversation{}, fmt.Errorf("decode LoCoMo %s %s: %w", id, dateKey, err)
			}
		}
		conversation.Sessions = append(conversation.Sessions, LoCoMoSession{Number: number, DateTime: dateTime, Turns: turns})
	}
	sort.Slice(conversation.Sessions, func(i, j int) bool { return conversation.Sessions[i].Number < conversation.Sessions[j].Number })
	if len(conversation.Sessions) == 0 {
		return LoCoMoConversation{}, fmt.Errorf("LoCoMo conversation %s has no sessions", id)
	}
	for _, question := range raw.QA {
		if question.Category < 1 || question.Category > 4 {
			continue
		}
		if strings.TrimSpace(question.Question) == "" || len(question.Evidence) == 0 {
			continue
		}
		conversation.Questions = append(conversation.Questions, question)
	}
	return conversation, nil
}

func sessionNumber(key string) (int, bool) {
	if !strings.HasPrefix(key, "session_") || strings.HasSuffix(key, "_date_time") || strings.HasSuffix(key, "_summary") {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimPrefix(key, "session_"))
	return number, err == nil && number > 0
}
