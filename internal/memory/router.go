package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ProviderBinding struct {
	Provider     Provider
	Read         bool
	Write        bool
	Required     bool
	Weight       float64
	Timeout      time.Duration
	MinRelevance float64
	MinScore     float64
	searchSlot   chan struct{}
	putSlot      chan struct{}
}

type SearchResult struct {
	Hits            []MemoryHit      `json:"hits"`
	ProviderErrors  []ProviderError  `json:"provider_errors,omitempty"`
	ProviderRecalls []ProviderRecall `json:"provider_recalls,omitempty"`
}

type searchResponse struct {
	binding      ProviderBinding
	hits         []MemoryHit
	err          error
	duration     time.Duration
	bulkheadBusy bool
}

type putResponse struct {
	binding ProviderBinding
	refs    []MemoryRef
	err     error
}

type PutResult struct {
	Refs           []MemoryRef     `json:"refs"`
	ProviderErrors []ProviderError `json:"provider_errors,omitempty"`
}

type Router struct {
	providers []ProviderBinding
	byName    map[string]ProviderBinding
}

func (r *Router) Close() error {
	var errs []error
	for _, binding := range r.providers {
		closer, ok := binding.Provider.(CloseProvider)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", binding.Provider.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func NewRouter(providers []ProviderBinding) (*Router, error) {
	byName := make(map[string]ProviderBinding, len(providers))
	normalized := make([]ProviderBinding, 0, len(providers))
	for _, binding := range providers {
		if binding.Provider == nil {
			return nil, errors.New("memory router provider is nil")
		}
		name := binding.Provider.Name()
		if name == "" {
			return nil, errors.New("memory router provider name is empty")
		}
		if _, exists := byName[name]; exists {
			return nil, fmt.Errorf("memory router provider %q is duplicated", name)
		}
		binding.searchSlot = make(chan struct{}, 1)
		binding.putSlot = make(chan struct{}, 1)
		byName[name] = binding
		normalized = append(normalized, binding)
	}
	return &Router{providers: normalized, byName: byName}, nil
}

func (r *Router) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	return r.SearchWithPolicy(ctx, query, SearchPolicy{})
}

func (r *Router) SearchWithPolicy(ctx context.Context, query SearchQuery, policy SearchPolicy) (SearchResult, error) {
	readable, err := r.readableBindings(policy)
	if err != nil {
		return SearchResult{}, err
	}
	if len(readable) == 0 {
		return SearchResult{}, errors.New("no readable memory providers are enabled")
	}
	resultLimit := query.Limit
	if policy.Limit > 0 {
		resultLimit = policy.Limit
	}
	query.Limit = providerCandidateLimit(resultLimit)
	if len(policy.Tiers) > 0 {
		query.Tiers = NormalizeTiers(policy.Tiers)
	}

	result, requiredErrs := collectSearchResponses(searchProviders(ctx, query, readable), policy)
	if len(requiredErrs) > 0 {
		return result, errors.Join(requiredErrs...)
	}
	sortSearchHits(result.Hits)
	if resultLimit > 0 && len(result.Hits) > resultLimit {
		result.Hits = result.Hits[:resultLimit]
	}
	return result, nil
}

func providerCandidateLimit(resultLimit int) int {
	if resultLimit <= 0 {
		return 0
	}
	const maxProviderCandidates = 100
	if resultLimit >= maxProviderCandidates {
		return resultLimit
	}
	if resultLimit >= maxProviderCandidates/3 {
		return maxProviderCandidates
	}
	return resultLimit * 3
}

func (r *Router) readableBindings(policy SearchPolicy) ([]ProviderBinding, error) {
	if len(policy.Providers) > 0 {
		return r.bindingsForRoutes(policy.Providers, "search")
	}
	var readable []ProviderBinding
	for _, binding := range r.providers {
		if binding.Read {
			readable = append(readable, binding)
		}
	}
	return readable, nil
}

func searchProviders(ctx context.Context, query SearchQuery, readable []ProviderBinding) []searchResponse {
	responses := make(chan searchResponse, len(readable))
	for _, binding := range readable {
		go func(binding ProviderBinding) {
			started := time.Now()
			providerCtx := ctx
			cancel := func() {}
			if binding.Timeout > 0 {
				providerCtx, cancel = context.WithTimeout(ctx, binding.Timeout)
			}
			defer cancel()
			select {
			case binding.searchSlot <- struct{}{}:
			case <-providerCtx.Done():
				responses <- searchResponse{binding: binding, err: providerCtx.Err(), duration: time.Since(started), bulkheadBusy: true}
				return
			}
			providerResult := make(chan searchResponse, 1)
			providerStarted := time.Now()
			go func() {
				defer func() { <-binding.searchSlot }()
				hits, err := binding.Provider.Search(providerCtx, query)
				providerResult <- searchResponse{binding: binding, hits: hits, err: err, duration: time.Since(providerStarted)}
			}()
			select {
			case response := <-providerResult:
				responses <- response
			case <-providerCtx.Done():
				responses <- searchResponse{binding: binding, err: providerCtx.Err(), duration: time.Since(providerStarted)}
			}
		}(binding)
	}

	collected := make([]searchResponse, 0, len(readable))
	for len(collected) < len(readable) {
		collected = append(collected, <-responses)
	}
	return collected
}

func collectSearchResponses(responses []searchResponse, policy SearchPolicy) (SearchResult, []error) {
	var result SearchResult
	var requiredErrs []error
	for _, response := range responses {
		result.ProviderRecalls = append(result.ProviderRecalls, providerRecallFromResponse(response))
		providerErr := appendSearchResponse(&result, response, policy)
		if providerErr != nil {
			requiredErrs = append(requiredErrs, providerErr)
		}
	}
	result.Hits = dedupeSearchHits(result.Hits)
	sort.Slice(result.ProviderRecalls, func(i, j int) bool { return result.ProviderRecalls[i].Provider < result.ProviderRecalls[j].Provider })
	return result, requiredErrs
}

func providerRecallFromResponse(response searchResponse) ProviderRecall {
	outcome := ProviderRecallSuccess
	if response.err != nil {
		outcome = ProviderRecallError
		if errors.Is(response.err, context.DeadlineExceeded) {
			outcome = ProviderRecallTimeout
		}
	}
	return ProviderRecall{
		Provider: response.binding.Provider.Name(), DurationMS: response.duration.Milliseconds(), Outcome: outcome,
		TimeoutMS: response.binding.Timeout.Milliseconds(), BulkheadBusy: response.bulkheadBusy,
	}
}

func appendSearchResponse(result *SearchResult, response searchResponse, policy SearchPolicy) error {
	name := response.binding.Provider.Name()
	if response.err != nil {
		result.ProviderErrors = append(result.ProviderErrors, ProviderError{
			Provider: name,
			Required: response.binding.Required,
			Op:       "search",
			Error:    response.err.Error(),
		})
		if response.binding.Required {
			return fmt.Errorf("%s: %w", name, response.err)
		}
		return nil
	}
	weight := response.binding.Weight
	if weight == 0 {
		weight = 1
	}
	minRelevance := policy.MinRelevance
	if response.binding.MinRelevance != 0 {
		minRelevance = response.binding.MinRelevance
	}
	minScore := policy.MinScore
	if response.binding.MinScore != 0 {
		minScore = response.binding.MinScore
	}
	for _, hit := range response.hits {
		hit.Provider = name
		hit.Tier = EffectiveHitTier(hit)
		hit.ExpiresAt = EffectiveHitExpiresAt(hit)
		if !hitMatchesPolicy(hit, policy, time.Now().UTC()) {
			continue
		}
		relevance := normalizedRelevance(hit)
		if relevance < minRelevance {
			continue
		}
		hit.Relevance = relevance
		hit.Score = relevance*weight + recencyScore(hit.CreatedAt, policy.RecencyBoost)
		if hit.Score < minScore {
			continue
		}
		result.Hits = append(result.Hits, hit)
	}
	return nil
}

func dedupeSearchHits(hits []MemoryHit) []MemoryHit {
	best := make(map[string]MemoryHit, len(hits))
	for _, hit := range hits {
		key := dedupeKey(hit)
		current, exists := best[key]
		if !exists || betterSearchHit(hit, current) {
			best[key] = hit
		}
	}
	deduped := make([]MemoryHit, 0, len(best))
	for _, hit := range best {
		deduped = append(deduped, hit)
	}
	return deduped
}

func betterSearchHit(candidate, current MemoryHit) bool {
	if candidate.Score != current.Score {
		return candidate.Score > current.Score
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	if candidate.Provider != current.Provider {
		return candidate.Provider < current.Provider
	}
	return candidate.ID < current.ID
}

func sortSearchHits(hits []MemoryHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].CreatedAt.After(hits[j].CreatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
}

func (r *Router) Put(ctx context.Context, item MemoryItem) (PutResult, error) {
	return r.PutWithPolicy(ctx, item, PutPolicy{})
}

func (r *Router) PutWithPolicy(ctx context.Context, item MemoryItem, policy PutPolicy) (PutResult, error) {
	return r.PutBatchWithPolicy(ctx, []MemoryItem{item}, policy)
}

func (r *Router) PutBatchWithPolicy(ctx context.Context, items []MemoryItem, policy PutPolicy) (PutResult, error) {
	writable, err := r.writableBindings(policy)
	if err != nil {
		return PutResult{}, err
	}
	if len(writable) == 0 {
		return PutResult{}, errors.New("no writable memory providers are enabled")
	}
	if len(items) == 0 {
		return PutResult{}, nil
	}
	items = applyPutPolicy(items, policy)
	items = admitLongTermMemories(items)
	return collectPutResponses(putProviders(ctx, writable, items))
}

func (r *Router) writableBindings(policy PutPolicy) ([]ProviderBinding, error) {
	if len(policy.Providers) > 0 {
		return r.bindingsForRoutes(policy.Providers, "put")
	}
	writable := make([]ProviderBinding, 0, len(r.providers))
	for _, binding := range r.providers {
		if binding.Write {
			writable = append(writable, binding)
		}
	}
	return writable, nil
}

func putProviders(ctx context.Context, writable []ProviderBinding, items []MemoryItem) []putResponse {
	responses := make(chan putResponse, len(writable))
	for _, binding := range writable {
		go func(binding ProviderBinding) {
			providerCtx := ctx
			cancel := func() {}
			if binding.Timeout > 0 {
				providerCtx, cancel = context.WithTimeout(ctx, binding.Timeout)
			}
			defer cancel()
			select {
			case binding.putSlot <- struct{}{}:
			case <-providerCtx.Done():
				responses <- putResponse{binding: binding, err: providerCtx.Err()}
				return
			}
			providerResult := make(chan putResponse, 1)
			go func() {
				defer func() { <-binding.putSlot }()
				refs, err := putBatch(providerCtx, binding.Provider, items)
				providerResult <- putResponse{binding: binding, refs: refs, err: err}
			}()
			select {
			case result := <-providerResult:
				responses <- result
			case <-providerCtx.Done():
				responses <- putResponse{binding: binding, err: providerCtx.Err()}
			}
		}(binding)
	}
	collected := make([]putResponse, 0, len(writable))
	for range writable {
		collected = append(collected, <-responses)
	}
	return collected
}

func collectPutResponses(responses []putResponse) (PutResult, error) {
	var result PutResult
	var requiredErrs []error
	for _, res := range responses {
		name := res.binding.Provider.Name()
		if res.err != nil {
			providerErr := ProviderError{
				Provider: name,
				Required: res.binding.Required,
				Op:       "put",
				Error:    res.err.Error(),
			}
			result.ProviderErrors = append(result.ProviderErrors, providerErr)
			if res.binding.Required {
				requiredErrs = append(requiredErrs, fmt.Errorf("%s: %w", name, res.err))
			}
			continue
		}
		for _, ref := range res.refs {
			ref.Provider = name
			result.Refs = append(result.Refs, ref)
		}
	}
	if len(requiredErrs) > 0 {
		return result, errors.Join(requiredErrs...)
	}
	sort.SliceStable(result.Refs, func(i, j int) bool {
		return result.Refs[i].Provider < result.Refs[j].Provider
	})
	return result, nil
}

func applyPutPolicy(items []MemoryItem, policy PutPolicy) []MemoryItem {
	if policy.Tier == "" && policy.ExpiresAfter <= 0 {
		return items
	}
	applied := append([]MemoryItem(nil), items...)
	for i := range applied {
		if policy.Tier != "" && applied[i].Tier == "" {
			applied[i].Tier = NormalizeTier(policy.Tier)
		}
		if policy.ExpiresAfter > 0 && applied[i].ExpiresAt == nil {
			base := applied[i].CreatedAt
			if base.IsZero() {
				base = time.Now().UTC()
			}
			expiresAt := base.Add(policy.ExpiresAfter).UTC()
			applied[i].ExpiresAt = &expiresAt
		}
	}
	return applied
}

func hitMatchesPolicy(hit MemoryHit, policy SearchPolicy, now time.Time) bool {
	if hit.ExpiresAt != nil && !hit.ExpiresAt.After(now) {
		return false
	}
	tiers := NormalizeTiers(policy.Tiers)
	if len(tiers) == 0 {
		return true
	}
	tier := EffectiveHitTier(hit)
	for _, allowed := range tiers {
		if tier == allowed {
			return true
		}
	}
	return false
}

func putBatch(ctx context.Context, provider Provider, items []MemoryItem) ([]MemoryRef, error) {
	if batchProvider, ok := provider.(BatchProvider); ok {
		return batchProvider.PutBatch(ctx, items)
	}
	refs := make([]MemoryRef, 0, len(items))
	for _, item := range items {
		ref, err := provider.Put(ctx, item)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (r *Router) bindingsForRoutes(routes []ProviderRoute, op string) ([]ProviderBinding, error) {
	bindings := make([]ProviderBinding, 0, len(routes))
	for _, route := range routes {
		if strings.TrimSpace(route.Name) == "" {
			continue
		}
		binding, ok := r.byName[route.Name]
		if !ok {
			return nil, fmt.Errorf("provider %q in %s policy is not enabled", route.Name, op)
		}
		binding.Required = route.Required
		binding.Timeout = route.Timeout
		if route.Weight != 0 {
			binding.Weight = route.Weight
		} else if binding.Weight == 0 {
			binding.Weight = 1
		}
		binding.MinRelevance = route.MinRelevance
		binding.MinScore = route.MinScore
		if op == "search" {
			binding.Read = true
		}
		if op == "put" {
			binding.Write = true
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func normalizedRelevance(hit MemoryHit) float64 {
	relevance := hit.Relevance
	if relevance == 0 && hit.Score > 0 {
		relevance = hit.Score
	}
	if relevance < 0 {
		return 0
	}
	if relevance > 1 {
		return 1
	}
	return relevance
}

func recencyScore(createdAt time.Time, boost float64) float64 {
	if boost <= 0 || createdAt.IsZero() {
		return 0
	}
	age := time.Since(createdAt)
	if age < 0 {
		return boost
	}
	return boost / (1 + age.Hours()/24)
}

func (r *Router) Health(ctx context.Context) ([]ProviderHealth, error) {
	if len(r.providers) == 0 {
		return nil, errors.New("no memory providers are enabled")
	}
	statuses := make([]ProviderHealth, 0, len(r.providers))
	var requiredErrs []error
	for _, binding := range r.providers {
		name := binding.Provider.Name()
		err := binding.Provider.Health(ctx)
		status := ProviderHealth{
			Provider: name,
			Required: binding.Required,
			OK:       err == nil,
		}
		if err != nil {
			status.Error = err.Error()
			if binding.Required {
				requiredErrs = append(requiredErrs, fmt.Errorf("%s: %w", name, err))
			}
		}
		statuses = append(statuses, status)
	}
	if len(requiredErrs) > 0 {
		return statuses, errors.Join(requiredErrs...)
	}
	return statuses, nil
}

func (r *Router) CleanupExpired(ctx context.Context, limit int) (CleanupExpiredResult, error) {
	if limit <= 0 {
		limit = 500
	}
	var result CleanupExpiredResult
	var requiredErrs []error
	for _, binding := range r.providers {
		cleanupProvider, ok := binding.Provider.(CleanupExpiredProvider)
		if !ok {
			continue
		}
		name := binding.Provider.Name()
		deleted, err := cleanupProvider.CleanupExpired(ctx, limit)
		if err != nil {
			result.ProviderErrors = append(result.ProviderErrors, ProviderError{
				Provider: name,
				Required: binding.Required,
				Op:       "cleanup_expired",
				Error:    err.Error(),
			})
			if binding.Required {
				requiredErrs = append(requiredErrs, fmt.Errorf("%s: %w", name, err))
			}
			continue
		}
		result.Deleted += deleted
	}
	if len(requiredErrs) > 0 {
		return result, errors.Join(requiredErrs...)
	}
	return result, nil
}

func dedupeKey(hit MemoryHit) string {
	if hit.Text != "" {
		return "text:" + strings.Join(strings.Fields(strings.ToLower(hit.Text)), " ")
	}
	return "id:" + hit.Provider + ":" + hit.ID
}
