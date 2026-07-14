package zep

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	zepgo "github.com/getzep/zep-go/v3"
	zepclient "github.com/getzep/zep-go/v3/client"
	"github.com/getzep/zep-go/v3/option"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const (
	defaultSearchScope = "episodes"
	defaultTimeout     = 30 * time.Second
)

type graphClient interface {
	Add(context.Context, *zepgo.AddDataRequest, ...option.RequestOption) (*zepgo.Episode, error)
	AddBatch(context.Context, *zepgo.AddDataBatchRequest, ...option.RequestOption) ([]*zepgo.Episode, error)
	Search(context.Context, *zepgo.GraphSearchQuery, ...option.RequestOption) (*zepgo.GraphSearchResults, error)
	Delete(context.Context, string, ...option.RequestOption) (*zepgo.SuccessResponse, error)
}

type Provider struct {
	name              string
	client            graphClient
	userID            string
	graphID           string
	searchScope       zepgo.GraphSearchScope
	maxCharacters     int
	sourceDescription string
}

func New(name string, cfg config.ProviderConfig) (*Provider, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(&http.Client{Timeout: defaultTimeout}),
	}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimSpace(cfg.BaseURL)))
	}
	client := zepclient.NewClient(opts...).Graph
	return newWithClient(name, cfg, client)
}

func newWithClient(name string, cfg config.ProviderConfig, client graphClient) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("zep provider name is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("zep provider api_key is required")
	}
	userID := strings.TrimSpace(cfg.UserID)
	graphID := strings.TrimSpace(cfg.GraphID)
	if userID == "" && graphID == "" {
		return nil, errors.New("zep provider requires user_id or graph_id")
	}
	if userID != "" && graphID != "" {
		return nil, errors.New("zep provider requires only one of user_id or graph_id")
	}
	if client == nil {
		return nil, errors.New("zep graph client is required")
	}
	scopeValue := strings.TrimSpace(cfg.SearchScope)
	if scopeValue == "" {
		scopeValue = defaultSearchScope
	}
	scope, err := zepgo.NewGraphSearchScopeFromString(scopeValue)
	if err != nil {
		return nil, fmt.Errorf("zep search_scope: %w", err)
	}
	if cfg.MaxCharacters < 0 {
		return nil, errors.New("zep max_characters must not be negative")
	}
	return &Provider{
		name:              name,
		client:            client,
		userID:            userID,
		graphID:           graphID,
		searchScope:       scope,
		maxCharacters:     cfg.MaxCharacters,
		sourceDescription: strings.TrimSpace(cfg.SourceDescription),
	}, nil
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return nil, errors.New("zep search query is required")
	}
	request := &zepgo.GraphSearchQuery{
		Query:            text,
		Scope:            &p.searchScope,
		ReturnRawResults: boolPtr(true),
	}
	p.applyTarget(request)
	if query.Limit > 0 && p.searchScope != zepgo.GraphSearchScopeAuto {
		request.Limit = intPtr(clampInt(query.Limit, 1, 50))
	}
	if p.maxCharacters > 0 {
		request.MaxCharacters = intPtr(p.maxCharacters)
	}

	result, err := p.client.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	hits := mapSearchResults(result)
	for i := range hits {
		hits[i].Provider = p.name
	}
	return hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	refs, err := p.PutBatch(ctx, []memory.MemoryItem{item})
	if err != nil {
		return memory.MemoryRef{}, err
	}
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("zep add did not return an episode uuid")
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
	episodes := make([]*zepgo.EpisodeData, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil, errors.New("memory text is required")
		}
		episode := &zepgo.EpisodeData{
			Data:              text,
			Type:              zepgo.GraphDataTypeText,
			Metadata:          toZepMetadata(item),
			SourceDescription: stringPtr(firstNonEmpty(p.sourceDescription, item.Source, "paxm")),
		}
		if !item.CreatedAt.IsZero() {
			episode.CreatedAt = stringPtr(item.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
		episodes = append(episodes, episode)
	}
	request := &zepgo.AddDataBatchRequest{
		Episodes: episodes,
	}
	p.applyBatchTarget(request)

	result, err := p.client.AddBatch(ctx, request)
	if err != nil {
		return nil, err
	}
	refs := make([]memory.MemoryRef, 0, len(result))
	for _, episode := range result {
		if episode == nil || strings.TrimSpace(episode.UUID) == "" {
			continue
		}
		refs = append(refs, memory.MemoryRef{Provider: p.name, ID: episode.UUID})
	}
	if len(refs) == 0 {
		return nil, errors.New("zep add batch did not return episode uuids")
	}
	return refs, nil
}

func (p *Provider) Health(ctx context.Context) error {
	return ctx.Err()
}

func (p *Provider) CleanupEvalScope(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strings.HasPrefix(p.graphID, "paxm-eval-") {
		return errors.New("zep eval cleanup requires a dedicated paxm-eval graph")
	}
	_, err := p.client.Delete(ctx, p.graphID)
	return err
}

func (p *Provider) applyTarget(request *zepgo.GraphSearchQuery) {
	if p.userID != "" {
		request.UserID = stringPtr(p.userID)
		return
	}
	request.GraphID = stringPtr(p.graphID)
}

func (p *Provider) applyAddTarget(request *zepgo.AddDataRequest) {
	if p.userID != "" {
		request.UserID = stringPtr(p.userID)
		return
	}
	request.GraphID = stringPtr(p.graphID)
}

func (p *Provider) applyBatchTarget(request *zepgo.AddDataBatchRequest) {
	if p.userID != "" {
		request.UserID = stringPtr(p.userID)
		return
	}
	request.GraphID = stringPtr(p.graphID)
}

func mapSearchResults(result *zepgo.GraphSearchResults) []memory.MemoryHit {
	if result == nil {
		return nil
	}
	var hits []memory.MemoryHit
	if result.Context != nil && strings.TrimSpace(*result.Context) != "" {
		hits = append(hits, memory.MemoryHit{
			ID:        "context",
			Text:      strings.TrimSpace(*result.Context),
			Relevance: 1,
			Score:     1,
			Source:    "zep:context",
			Metadata: map[string]string{
				"zep_type": "context",
			},
		})
	}
	for _, episode := range result.Episodes {
		hits = appendNonEmptyHit(hits, hitFromEpisode(episode))
	}
	for _, edge := range result.Edges {
		hits = appendNonEmptyHit(hits, hitFromEdge(edge))
	}
	for _, node := range result.Nodes {
		hits = appendNonEmptyHit(hits, hitFromNode(node))
	}
	for _, observation := range result.Observations {
		hits = appendNonEmptyHit(hits, hitFromObservation(observation))
	}
	for _, summary := range result.ThreadSummaries {
		hits = appendNonEmptyHit(hits, hitFromThreadSummary(summary))
	}
	return hits
}

func hitFromEpisode(episode *zepgo.Episode) memory.MemoryHit {
	if episode == nil {
		return memory.MemoryHit{}
	}
	relevance, rawScore, rawScoreKind := relevance(episode.Relevance, episode.Score, episode.SelectionRank)
	metadata := stringMapFromInterfaces(episode.Metadata)
	metadata["zep_type"] = "episode"
	if episode.Processed != nil {
		metadata["zep_processed"] = strconv.FormatBool(*episode.Processed)
	}
	if episode.Source != nil {
		metadata["zep_source"] = string(*episode.Source)
	}
	if episode.SourceDescription != nil {
		metadata["zep_source_description"] = *episode.SourceDescription
	}
	return memory.MemoryHit{
		ID:           episode.UUID,
		Text:         strings.TrimSpace(episode.Content),
		Relevance:    relevance,
		Score:        relevance,
		RawScore:     rawScore,
		RawScoreKind: rawScoreKind,
		Source:       "zep:episode",
		Metadata:     metadata,
		CreatedAt:    parseZepTime(episode.CreatedAt),
	}
}

func hitFromEdge(edge *zepgo.EntityEdge) memory.MemoryHit {
	if edge == nil {
		return memory.MemoryHit{}
	}
	relevance, rawScore, rawScoreKind := relevance(edge.Relevance, edge.Score, edge.SelectionRank)
	metadata := stringMapFromInterfaces(edge.Attributes)
	metadata["zep_type"] = "edge"
	metadata["zep_edge_name"] = edge.Name
	metadata["zep_source_node_uuid"] = edge.SourceNodeUUID
	metadata["zep_target_node_uuid"] = edge.TargetNodeUUID
	if len(edge.Episodes) > 0 {
		metadata["zep_episode_uuids"] = strings.Join(edge.Episodes, ",")
	}
	return memory.MemoryHit{
		ID:           edge.UUID,
		Text:         strings.TrimSpace(firstNonEmpty(edge.Fact, edge.Name)),
		Relevance:    relevance,
		Score:        relevance,
		RawScore:     rawScore,
		RawScoreKind: rawScoreKind,
		Source:       "zep:edge",
		Metadata:     metadata,
		CreatedAt:    parseZepTime(edge.CreatedAt),
	}
}

func hitFromNode(node *zepgo.EntityNode) memory.MemoryHit {
	if node == nil {
		return memory.MemoryHit{}
	}
	relevance, rawScore, rawScoreKind := relevance(node.Relevance, node.Score, node.SelectionRank)
	metadata := stringMapFromInterfaces(node.Attributes)
	metadata["zep_type"] = "node"
	if len(node.Labels) > 0 {
		metadata["zep_labels"] = strings.Join(node.Labels, ",")
	}
	return memory.MemoryHit{
		ID:           node.UUID,
		Text:         joinText(node.Name, node.Summary),
		Relevance:    relevance,
		Score:        relevance,
		RawScore:     rawScore,
		RawScoreKind: rawScoreKind,
		Source:       "zep:node",
		Metadata:     metadata,
		CreatedAt:    parseZepTime(node.CreatedAt),
	}
}

func hitFromObservation(node *zepgo.DerivedNode) memory.MemoryHit {
	if node == nil {
		return memory.MemoryHit{}
	}
	relevance, rawScore, rawScoreKind := relevance(node.Relevance, node.Score, node.SelectionRank)
	metadata := stringMapFromInterfaces(node.Attributes)
	metadata["zep_type"] = "observation"
	if len(node.Labels) > 0 {
		metadata["zep_labels"] = strings.Join(node.Labels, ",")
	}
	if len(node.EpisodeIDs) > 0 {
		metadata["zep_episode_uuids"] = strings.Join(node.EpisodeIDs, ",")
	}
	return memory.MemoryHit{
		ID:           node.UUID,
		Text:         joinText(node.Name, derefString(node.Summary)),
		Relevance:    relevance,
		Score:        relevance,
		RawScore:     rawScore,
		RawScoreKind: rawScoreKind,
		Source:       "zep:observation",
		Metadata:     metadata,
		CreatedAt:    parseZepTime(node.CreatedAt),
	}
}

func hitFromThreadSummary(summary *zepgo.GraphitiSagaNode) memory.MemoryHit {
	if summary == nil {
		return memory.MemoryHit{}
	}
	relevance, rawScore, rawScoreKind := relevance(summary.Relevance, summary.Score, summary.SelectionRank)
	metadata := map[string]string{"zep_type": "thread_summary"}
	if len(summary.Labels) > 0 {
		metadata["zep_labels"] = strings.Join(summary.Labels, ",")
	}
	if summary.LastSummarizedAt != nil {
		metadata["zep_last_summarized_at"] = *summary.LastSummarizedAt
	}
	return memory.MemoryHit{
		ID:           summary.UUID,
		Text:         joinText(summary.Name, derefString(summary.Summary)),
		Relevance:    relevance,
		Score:        relevance,
		RawScore:     rawScore,
		RawScoreKind: rawScoreKind,
		Source:       "zep:thread_summary",
		Metadata:     metadata,
		CreatedAt:    parseZepTime(summary.CreatedAt),
	}
}

func appendNonEmptyHit(hits []memory.MemoryHit, hit memory.MemoryHit) []memory.MemoryHit {
	if strings.TrimSpace(hit.Text) == "" || strings.TrimSpace(hit.ID) == "" {
		return hits
	}
	return append(hits, hit)
}

func relevance(relevanceValue, scoreValue *float64, selectionRank *int) (float64, *float64, string) {
	if relevanceValue != nil {
		raw := *relevanceValue
		if scoreValue != nil {
			raw = *scoreValue
		}
		return clampFloat(*relevanceValue, 0, 1), &raw, "zep_relevance"
	}
	if scoreValue != nil {
		raw := *scoreValue
		if *scoreValue >= 0 && *scoreValue <= 1 {
			return *scoreValue, &raw, "zep_score"
		}
		if *scoreValue > 1 {
			return 1 / (1 + *scoreValue), &raw, "zep_rank_score"
		}
		return 0, &raw, "zep_score"
	}
	if selectionRank != nil && *selectionRank >= 0 {
		raw := float64(*selectionRank)
		return 1 / (1 + raw), &raw, "zep_selection_rank"
	}
	return 1, nil, "zep_unscored"
}

func toZepMetadata(item memory.MemoryItem) map[string]interface{} {
	metadata := make(map[string]interface{})
	addMetadata(metadata, "paxm_id", item.ID)
	addMetadata(metadata, "paxm_source", item.Source)
	addMetadata(metadata, "paxm_tier", string(memory.NormalizeTier(item.Tier)))
	if item.ExpiresAt != nil {
		addMetadata(metadata, "paxm_expires_at", item.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	for _, key := range []string{memory.MetadataUserID, memory.MetadataAgentID, memory.MetadataScopeType, memory.MetadataScopeID} {
		addMetadata(metadata, key, item.Metadata[key])
	}
	keys := make([]string, 0, len(item.Metadata))
	for key := range item.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		addMetadata(metadata, key, item.Metadata[key])
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func addMetadata(metadata map[string]interface{}, key, value string) {
	if len(metadata) >= 10 || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	metadata[key] = value
}

func stringMapFromInterfaces(values map[string]interface{}) map[string]string {
	metadata := make(map[string]string)
	for key, value := range values {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		metadata[key] = fmt.Sprint(value)
	}
	return metadata
}

func parseZepTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func joinText(parts ...string) string {
	var lines []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			lines = append(lines, strings.TrimSpace(part))
		}
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func intPtr(value int) *int {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
