package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type AgentArm string

const (
	AgentArmControl AgentArm = "control"
	AgentArmPassive AgentArm = "passive"
	AgentArmActive  AgentArm = "active"
)

type AgentRequest struct {
	AgentName      string
	Arm            AgentArm
	QuestionID     string
	Question       string
	Prompt         string
	Workspace      string
	PaxmConfigPath string
	RecallEnabled  bool
	WriteEnabled   bool
}

type AgentResponse struct {
	Text         string  `json:"text"`
	SessionID    string  `json:"session_id,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	DurationMS   int64   `json:"duration_ms"`
	RecallUsed   bool    `json:"recall_used,omitempty"`
	Model        string  `json:"model,omitempty"`
}

type AgentExecutor interface {
	Execute(context.Context, AgentRequest) (AgentResponse, error)
}

type AgentWriteFlusher interface {
	FlushWrites(context.Context, string) error
}

type LoCoMoAgentOptions struct {
	Config         config.Config
	Provider       string
	RunID          string
	ManifestDir    string
	AgentName      string
	Arms           []AgentArm
	MaxQuestions   int
	MatchThreshold float64
	KeepMemory     bool
	Settle         time.Duration
}

type LoCoMoAgentRunner struct {
	BuildProvider func(string, config.ProviderConfig) (memory.Provider, error)
	Agent         AgentExecutor
}

type AgentTrial struct {
	ConversationID string        `json:"conversation_id"`
	QuestionID     string        `json:"question_id"`
	Question       string        `json:"question"`
	Reference      string        `json:"reference"`
	Category       int           `json:"category"`
	Arm            AgentArm      `json:"arm"`
	Response       AgentResponse `json:"response"`
	TokenF1        float64       `json:"token_f1"`
	ExactMatch     bool          `json:"exact_match"`
	Matched        bool          `json:"matched"`
	Error          string        `json:"error,omitempty"`
}

type AgentArmSummary struct {
	Arm              AgentArm `json:"arm"`
	Trials           int      `json:"trials"`
	Matched          int      `json:"matched"`
	Errors           int      `json:"errors"`
	RecallUsed       int      `json:"recall_used"`
	UsefulRecall     int      `json:"useful_recall"`
	Accuracy         float64  `json:"accuracy"`
	MeanF1           float64  `json:"mean_f1"`
	ExactMatch       float64  `json:"exact_match"`
	RecallUseRate    float64  `json:"recall_use_rate"`
	UsefulRecallRate float64  `json:"useful_recall_rate"`
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	Cost             float64  `json:"cost"`
	DurationMS       int64    `json:"duration_ms"`
}

type LoCoMoAgentResult struct {
	Benchmark      string            `json:"benchmark"`
	DatasetVersion string            `json:"dataset_version"`
	Agent          string            `json:"agent"`
	Provider       string            `json:"provider"`
	Model          string            `json:"model,omitempty"`
	WriteCanary    bool              `json:"write_canary"`
	QuestionCount  int               `json:"question_count"`
	TrialCount     int               `json:"trial_count"`
	PassiveLift    float64           `json:"passive_lift,omitempty"`
	ActiveLift     float64           `json:"active_lift,omitempty"`
	DurationMS     int64             `json:"duration_ms"`
	Summaries      []AgentArmSummary `json:"summaries"`
	Trials         []AgentTrial      `json:"trials"`
}

func (r LoCoMoAgentRunner) Run(ctx context.Context, dataset LoCoMoDataset, opts LoCoMoAgentOptions) (result LoCoMoAgentResult, err error) {
	if r.BuildProvider == nil || r.Agent == nil {
		return result, errors.New("LoCoMo agent eval requires provider and agent executors")
	}
	if strings.TrimSpace(opts.AgentName) == "" {
		return result, errors.New("LoCoMo agent name is required")
	}
	if len(opts.Arms) == 0 {
		opts.Arms = []AgentArm{AgentArmControl, AgentArmPassive, AgentArmActive}
	}
	if opts.MatchThreshold <= 0 {
		opts.MatchThreshold = 0.5
	}
	started := time.Now()
	result = LoCoMoAgentResult{Benchmark: "locomo-agent", DatasetVersion: "official-locomo10", Agent: opts.AgentName, Provider: opts.Provider}
	remaining := opts.MaxQuestions
	for _, conversation := range dataset.Conversations {
		questions := conversation.Questions
		if opts.MaxQuestions > 0 {
			if remaining <= 0 {
				break
			}
			if len(questions) > remaining {
				questions = questions[:remaining]
			}
			remaining -= len(questions)
		}
		if len(questions) == 0 {
			continue
		}
		trials, runErr := r.runAgentConversation(ctx, conversation, questions, opts)
		result.Trials = append(result.Trials, trials...)
		if runErr != nil {
			err = errors.Join(err, runErr)
		}
		if len(trials) > 0 && result.Model == "" {
			result.Model = trials[0].Response.Model
		}
		if _, ok := r.Agent.(AgentWriteFlusher); ok && runErr == nil {
			result.WriteCanary = true
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.aggregate(opts.Arms)
	return result, err
}

func (r LoCoMoAgentRunner) runAgentConversation(ctx context.Context, conversation LoCoMoConversation, questions []LoCoMoQuestion, opts LoCoMoAgentOptions) (trials []AgentTrial, err error) {
	runID := sanitizeScopeID(opts.RunID + "-" + conversation.ID)
	scope, err := PrepareProviderScope(opts.Config, opts.Provider, ScopeOptions{RunID: runID, ManifestDir: opts.ManifestDir, KeepMemory: opts.KeepMemory})
	if err != nil {
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	provider, err := r.BuildProvider(opts.Provider, scope.Config.Providers[opts.Provider])
	if err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := CleanupProviderScope(cleanupCtx, scope, provider); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if err := validateEvalCleanupCapability(scope, provider); err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	if err := ingestLoCoMoItems(ctx, provider, loCoMoMemoryItems(conversation), scope); err != nil {
		_ = scope.SetStatus(EvalStatusFailed, err)
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	if opts.Settle > 0 {
		timer := time.NewTimer(opts.Settle)
		select {
		case <-ctx.Done():
			timer.Stop()
			return failedAgentTrials(conversation.ID, questions, opts.Arms, ctx.Err()), ctx.Err()
		case <-timer.C:
		}
	}
	runDir := filepath.Dir(scope.ManifestPath)
	configPath := filepath.Join(runDir, "config.yaml")
	prepareAgentEvalConfig(&scope.Config, configPath, opts.AgentName)
	if err := config.Save(configPath, scope.Config); err != nil {
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	workspace := filepath.Join(runDir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return failedAgentTrials(conversation.ID, questions, opts.Arms, err), err
	}
	canary := "PAXM_EVAL_WRITE_" + sanitizeScopeID(runID)
	if flusher, ok := r.Agent.(AgentWriteFlusher); ok {
		_, canaryErr := r.Agent.Execute(ctx, AgentRequest{AgentName: opts.AgentName, Arm: AgentArmControl, QuestionID: "write-canary", Workspace: workspace, PaxmConfigPath: configPath, WriteEnabled: true, Prompt: "Remember this exact token for later: " + canary + ". Reply with exactly STORED."})
		if canaryErr == nil {
			canaryErr = flusher.FlushWrites(ctx, configPath)
		}
		if canaryErr == nil {
			var ref memory.MemoryRef
			ref, canaryErr = waitForAgentWrite(ctx, provider, canary, 10*time.Second)
			if canaryErr == nil {
				canaryErr = scope.RecordRefs([]memory.MemoryRef{ref})
			}
		}
		if canaryErr != nil {
			_ = scope.SetStatus(EvalStatusFailed, canaryErr)
			return failedAgentTrials(conversation.ID, questions, opts.Arms, canaryErr), fmt.Errorf("agent write canary: %w", canaryErr)
		}
	}
	for index, question := range questions {
		reference := loCoMoAnswer(question.Answer)
		questionID := fmt.Sprintf("%s-q%04d", conversation.ID, index+1)
		for _, arm := range opts.Arms {
			request := AgentRequest{
				AgentName: opts.AgentName, Arm: arm, QuestionID: questionID, Question: question.Question,
				Prompt: agentQuestionPrompt(question.Question, arm), Workspace: workspace, PaxmConfigPath: configPath,
				RecallEnabled: arm == AgentArmPassive,
			}
			response, executeErr := r.Agent.Execute(ctx, request)
			trial := AgentTrial{ConversationID: conversation.ID, QuestionID: questionID, Question: question.Question, Reference: reference, Category: question.Category, Arm: arm, Response: response}
			if executeErr != nil {
				trial.Error = executeErr.Error()
			} else {
				trial.TokenF1 = answerTokenF1(response.Text, reference)
				trial.ExactMatch = normalizeAnswer(response.Text) == normalizeAnswer(reference)
				trial.Matched = trial.TokenF1 >= opts.MatchThreshold
			}
			trials = append(trials, trial)
		}
	}
	if err := scope.SetStatus(EvalStatusComplete, nil); err != nil {
		return trials, err
	}
	return trials, nil
}

func prepareAgentEvalConfig(cfg *config.Config, configPath, agentName string) {
	template := config.DefaultConfig(configPath).Agents["claude"]
	template.Enabled = true
	for event, hook := range template.Hooks {
		hook.Write.Enabled = false
		hook.Write.Buffer.Enabled = false
		if event == "turn_end" {
			hook.Write.Enabled = true
			hook.Write.Buffer.Enabled = true
			hook.Write.Buffer.Flush = true
		}
		template.Hooks[event] = hook
	}
	cfg.Agents = map[string]config.AgentConfig{agentName: template}
	cfg.Telemetry.Dir = filepath.Join(filepath.Dir(configPath), "state")
	cfg.CaptureQueue.Path = filepath.Join(filepath.Dir(configPath), "capture.sqlite")
}

func waitForAgentWrite(ctx context.Context, provider memory.Provider, canary string, timeout time.Duration) (memory.MemoryRef, error) {
	deadline := time.Now().Add(timeout)
	for {
		hits, err := provider.Search(ctx, memory.SearchQuery{Text: canary, Limit: 10})
		if err == nil {
			for _, hit := range hits {
				if strings.Contains(hit.Text, canary) {
					return memory.MemoryRef{Provider: provider.Name(), ID: hit.ID}, nil
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return memory.MemoryRef{}, err
			}
			return memory.MemoryRef{}, errors.New("canary was not persisted by the agent hook")
		}
		select {
		case <-ctx.Done():
			return memory.MemoryRef{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func agentQuestionPrompt(question string, arm AgentArm) string {
	prefix := "Answer the benchmark question with only the concise factual answer. Do not explain your reasoning or inspect the workspace."
	if arm == AgentArmActive {
		prefix += " Before answering, call the paxm_recall memory tool using the complete question, then answer from relevant recalled evidence."
	}
	return prefix + "\n\nQuestion: " + strings.TrimSpace(question)
}

func failedAgentTrials(conversationID string, questions []LoCoMoQuestion, arms []AgentArm, runErr error) []AgentTrial {
	var trials []AgentTrial
	for index, question := range questions {
		for _, arm := range arms {
			trials = append(trials, AgentTrial{ConversationID: conversationID, QuestionID: fmt.Sprintf("%s-q%04d", conversationID, index+1), Question: question.Question, Reference: loCoMoAnswer(question.Answer), Category: question.Category, Arm: arm, Error: runErr.Error()})
		}
	}
	return trials
}

func loCoMoAnswer(raw json.RawMessage) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func answerTokenF1(actual, expected string) float64 {
	actualTokens := strings.Fields(normalizeAnswer(actual))
	expectedTokens := strings.Fields(normalizeAnswer(expected))
	if len(actualTokens) == 0 || len(expectedTokens) == 0 {
		if len(actualTokens) == len(expectedTokens) {
			return 1
		}
		return 0
	}
	counts := make(map[string]int)
	for _, token := range expectedTokens {
		counts[token]++
	}
	common := 0
	for _, token := range actualTokens {
		if counts[token] > 0 {
			common++
			counts[token]--
		}
	}
	if common == 0 {
		return 0
	}
	precision := float64(common) / float64(len(actualTokens))
	recall := float64(common) / float64(len(expectedTokens))
	return 2 * precision * recall / (precision + recall)
}

func normalizeAnswer(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	filtered := words[:0]
	for _, word := range words {
		if word != "a" && word != "an" && word != "the" {
			filtered = append(filtered, word)
		}
	}
	return strings.Join(filtered, " ")
}

func (r *LoCoMoAgentResult) aggregate(order []AgentArm) {
	r.TrialCount = len(r.Trials)
	questions := make(map[string]bool)
	groups := make(map[AgentArm]*AgentArmSummary)
	for _, arm := range order {
		groups[arm] = &AgentArmSummary{Arm: arm}
	}
	for _, trial := range r.Trials {
		questions[trial.ConversationID+"/"+trial.QuestionID] = true
		group := groups[trial.Arm]
		if group == nil {
			group = &AgentArmSummary{Arm: trial.Arm}
			groups[trial.Arm] = group
		}
		group.Trials++
		if trial.Error != "" {
			group.Errors++
		}
		if trial.Matched {
			group.Matched++
		}
		if trial.Response.RecallUsed {
			group.RecallUsed++
			if trial.Matched {
				group.UsefulRecall++
			}
		}
		if trial.ExactMatch {
			group.ExactMatch++
		}
		group.MeanF1 += trial.TokenF1
		group.InputTokens += trial.Response.InputTokens
		group.OutputTokens += trial.Response.OutputTokens
		group.Cost += trial.Response.Cost
		group.DurationMS += trial.Response.DurationMS
	}
	r.QuestionCount = len(questions)
	for _, arm := range order {
		group := groups[arm]
		if group == nil {
			continue
		}
		if group.Trials > 0 {
			denominator := float64(group.Trials)
			group.Accuracy = float64(group.Matched) / denominator
			group.MeanF1 /= denominator
			group.ExactMatch /= denominator
			group.RecallUseRate = float64(group.RecallUsed) / denominator
			if group.RecallUsed > 0 {
				group.UsefulRecallRate = float64(group.UsefulRecall) / float64(group.RecallUsed)
			}
			group.Cost = math.Round(group.Cost*1e9) / 1e9
		}
		r.Summaries = append(r.Summaries, *group)
	}
	control := summaryAccuracy(groups[AgentArmControl])
	r.PassiveLift = summaryAccuracy(groups[AgentArmPassive]) - control
	r.ActiveLift = summaryAccuracy(groups[AgentArmActive]) - control
	sort.SliceStable(r.Trials, func(i, j int) bool {
		if r.Trials[i].QuestionID != r.Trials[j].QuestionID {
			return r.Trials[i].QuestionID < r.Trials[j].QuestionID
		}
		return armIndex(order, r.Trials[i].Arm) < armIndex(order, r.Trials[j].Arm)
	})
}

func summaryAccuracy(summary *AgentArmSummary) float64 {
	if summary == nil {
		return 0
	}
	return summary.Accuracy
}

func armIndex(order []AgentArm, arm AgentArm) int {
	for index, candidate := range order {
		if candidate == arm {
			return index
		}
	}
	return len(order)
}
