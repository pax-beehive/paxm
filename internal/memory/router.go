package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ProviderBinding struct {
	Provider     Provider
	Read         bool
	Write        bool
	Required     bool
	Weight       float64
	MinRelevance float64
	MinScore     float64
}

type SearchResult struct {
	Hits           []MemoryHit     `json:"hits"`
	ProviderErrors []ProviderError `json:"provider_errors,omitempty"`
}

type searchResponse struct {
	binding ProviderBinding
	hits    []MemoryHit
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

func NewRouter(providers []ProviderBinding) (*Router, error) {
	byName := make(map[string]ProviderBinding, len(providers))
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
		byName[name] = binding
	}
	return &Router{providers: append([]ProviderBinding(nil), providers...), byName: byName}, nil
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
	if policy.Limit > 0 {
		query.Limit = policy.Limit
	}

	result, requiredErrs := collectSearchResponses(searchProviders(ctx, query, readable), policy)
	if len(requiredErrs) > 0 {
		return result, errors.Join(requiredErrs...)
	}
	sortSearchHits(result.Hits)
	if query.Limit > 0 && len(result.Hits) > query.Limit {
		result.Hits = result.Hits[:query.Limit]
	}
	return result, nil
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
	var wg sync.WaitGroup
	for _, binding := range readable {
		wg.Add(1)
		go func(binding ProviderBinding) {
			defer wg.Done()
			hits, err := binding.Provider.Search(ctx, query)
			responses <- searchResponse{binding: binding, hits: hits, err: err}
		}(binding)
	}
	wg.Wait()
	close(responses)

	collected := make([]searchResponse, 0, len(readable))
	for response := range responses {
		collected = append(collected, response)
	}
	return collected
}

func collectSearchResponses(responses []searchResponse, policy SearchPolicy) (SearchResult, []error) {
	var result SearchResult
	var requiredErrs []error
	seen := make(map[string]struct{})
	for _, response := range responses {
		providerErr := appendSearchResponse(&result, seen, response, policy)
		if providerErr != nil {
			requiredErrs = append(requiredErrs, providerErr)
		}
	}
	return result, requiredErrs
}

func appendSearchResponse(result *SearchResult, seen map[string]struct{}, response searchResponse, policy SearchPolicy) error {
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
		relevance := normalizedRelevance(hit)
		if relevance < minRelevance {
			continue
		}
		hit.Relevance = relevance
		hit.Score = relevance*weight + recencyScore(hit.CreatedAt, policy.RecencyBoost)
		if hit.Score < minScore {
			continue
		}
		key := dedupeKey(hit)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result.Hits = append(result.Hits, hit)
	}
	return nil
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
	var writable []ProviderBinding
	var err error
	if len(policy.Providers) > 0 {
		writable, err = r.bindingsForRoutes(policy.Providers, "put")
	} else {
		for _, binding := range r.providers {
			if binding.Write {
				writable = append(writable, binding)
			}
		}
	}
	if err != nil {
		return PutResult{}, err
	}
	if len(writable) == 0 {
		return PutResult{}, errors.New("no writable memory providers are enabled")
	}
	if len(items) == 0 {
		return PutResult{}, nil
	}

	type response struct {
		binding ProviderBinding
		refs    []MemoryRef
		err     error
	}

	responses := make(chan response, len(writable))
	var wg sync.WaitGroup
	for _, binding := range writable {
		wg.Add(1)
		go func(binding ProviderBinding) {
			defer wg.Done()
			refs, err := putBatch(ctx, binding.Provider, items)
			responses <- response{binding: binding, refs: refs, err: err}
		}(binding)
	}
	wg.Wait()
	close(responses)

	var result PutResult
	var requiredErrs []error
	for res := range responses {
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

func dedupeKey(hit MemoryHit) string {
	if hit.Text != "" {
		return "text:" + strings.Join(strings.Fields(strings.ToLower(hit.Text)), " ")
	}
	return "id:" + hit.Provider + ":" + hit.ID
}
