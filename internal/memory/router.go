package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
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

// Clock supplies the current time. Nil clocks fall back to time.Now.
type Clock func() time.Time

// RouterOption customizes a Router at construction time.
type RouterOption func(*Router)

// WithClock makes the router read the current time from clock instead of
// time.Now, which keeps ranking and expiry decisions deterministic in tests.
func WithClock(clock Clock) RouterOption {
	return func(r *Router) {
		if clock != nil {
			r.clock = clock
		}
	}
}

// SequenceAllocator reserves monotonically increasing sequence values across
// runtime lifetimes for session-scoped writes.
type SequenceAllocator interface {
	Next(sessionID string, floor int64) (int64, error)
	Close() error
}

// WithSequenceAllocator persists automatically assigned session sequences.
func WithSequenceAllocator(allocator SequenceAllocator) RouterOption {
	return func(r *Router) {
		r.sequenceAllocator = allocator
	}
}

type Router struct {
	providers         []ProviderBinding
	byName            map[string]ProviderBinding
	clock             Clock
	sequenceMu        sync.Mutex
	sessionSequences  map[string]int64
	sequenceAllocator SequenceAllocator
}

func (r *Router) nowUTC() time.Time {
	if r.clock != nil {
		return r.clock().UTC()
	}
	return time.Now().UTC()
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
	if r.sequenceAllocator != nil {
		if err := r.sequenceAllocator.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close session sequence allocator: %w", err))
		}
	}
	return errors.Join(errs...)
}

func NewRouter(providers []ProviderBinding, opts ...RouterOption) (*Router, error) {
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
	router := &Router{providers: normalized, byName: byName, sessionSequences: map[string]int64{}}
	for _, opt := range opts {
		opt(router)
	}
	return router, nil
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

	result, requiredErrs := collectSearchResponses(searchProviders(ctx, query, readable), policy, r.nowUTC())
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
	responses := make([]searchResponse, len(readable))
	var wg sync.WaitGroup
	for i, binding := range readable {
		wg.Go(func() {
			responses[i] = runSearch(ctx, query, binding)
		})
	}
	wg.Wait()
	return responses
}

// runSearch executes one provider search under the binding's bulkhead and
// timeout. The bulkhead slot is held until the provider call actually returns,
// so a stuck provider keeps occupying its slot and subsequent searches fail
// fast instead of piling up behind it.
func runSearch(ctx context.Context, query SearchQuery, binding ProviderBinding) searchResponse {
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
		return searchResponse{binding: binding, err: providerCtx.Err(), duration: time.Since(started), bulkheadBusy: true}
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
		return response
	case <-providerCtx.Done():
		return searchResponse{binding: binding, err: providerCtx.Err(), duration: time.Since(providerStarted)}
	}
}

func collectSearchResponses(responses []searchResponse, policy SearchPolicy, now time.Time) (SearchResult, []error) {
	var result SearchResult
	var requiredErrs []error
	for _, response := range responses {
		recall := providerRecallFromResponse(response)
		providerErr := appendSearchResponse(&result, response, policy, &recall, now)
		result.ProviderRecalls = append(result.ProviderRecalls, recall)
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
		Provider:       response.binding.Provider.Name(),
		DurationMS:     response.duration.Milliseconds(),
		Outcome:        outcome,
		TimeoutMS:      response.binding.Timeout.Milliseconds(),
		BulkheadBusy:   response.bulkheadBusy,
		CandidateCount: len(response.hits),
		RawScoreKinds:  rawScoreKinds(response.hits),
	}
}

func appendSearchResponse(result *SearchResult, response searchResponse, policy SearchPolicy, recall *ProviderRecall, now time.Time) error {
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
	eligible := make([]MemoryHit, 0, len(response.hits))
	for _, hit := range response.hits {
		hit.Provider = name
		hit = ApplyHitAttribution(hit)
		hit.Tier = EffectiveHitTier(hit)
		hit.ExpiresAt = EffectiveHitExpiresAt(hit)
		hit.Relevance = normalizedRelevance(hit)
		if !hitMatchesPolicy(hit, policy, now) {
			continue
		}
		if hit.Relevance < minRelevance {
			continue
		}
		recency := recencyScore(hit.CreatedAt, policy.RecencyBoost, now)
		hit.Score = applyWeightAndRecency(hit.Relevance, weight, recency)
		if hit.Score < minScore {
			continue
		}
		eligible = append(eligible, hit)
	}
	if recall != nil {
		recall.EligibleCount = len(eligible)
	}
	for _, hit := range calibrateProviderHits(eligible) {
		recency := recencyScore(hit.CreatedAt, policy.RecencyBoost, now)
		hit.rankingScore = applyWeightAndRecency(hit.rankingScore, weight, recency)
		result.Hits = append(result.Hits, hit)
	}
	return nil
}

func rawScoreKinds(hits []MemoryHit) []string {
	seen := make(map[string]struct{})
	for _, hit := range hits {
		if kind := strings.TrimSpace(hit.RawScoreKind); kind != "" {
			seen[kind] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
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
	if candidate.rankingScore != current.rankingScore {
		return candidate.rankingScore > current.rankingScore
	}
	if candidate.Score != current.Score {
		return candidate.Score > current.Score
	}
	if candidate.Relevance != current.Relevance {
		return candidate.Relevance > current.Relevance
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
		if hits[i].rankingScore != hits[j].rankingScore {
			return hits[i].rankingScore > hits[j].rankingScore
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if !hits[i].CreatedAt.Equal(hits[j].CreatedAt) {
			return hits[i].CreatedAt.After(hits[j].CreatedAt)
		}
		if hits[i].Provider != hits[j].Provider {
			return hits[i].Provider < hits[j].Provider
		}
		return hits[i].ID < hits[j].ID
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
	now := r.nowUTC()
	items = applyPutPolicy(items, policy, now)
	for i := range items {
		items[i] = PrepareProviderItem(items[i])
	}
	items = admitLongTermMemories(items, now)
	if err := r.assignSessionSequences(items); err != nil {
		return PutResult{}, err
	}
	return collectPutResponses(putProviders(ctx, writable, items))
}

func (r *Router) assignSessionSequences(items []MemoryItem) error {
	r.sequenceMu.Lock()
	defer r.sequenceMu.Unlock()
	for index := range items {
		item := &items[index]
		if strings.TrimSpace(item.Metadata[MetadataSequence]) != "" {
			continue
		}
		sessionID := strings.TrimSpace(item.Origin.SessionID)
		if sessionID == "" {
			continue
		}
		sequence := item.CreatedAt.UTC().UnixNano()
		if sequence < 1 {
			sequence = 1
		}
		var err error
		if r.sequenceAllocator != nil {
			sequence, err = r.sequenceAllocator.Next(sessionID, sequence)
			if err != nil {
				return fmt.Errorf("allocate session sequence: %w", err)
			}
		} else if previous := r.sessionSequences[sessionID]; sequence <= previous {
			sequence = previous + 1
		}
		metadata := cloneMetadata(item.Metadata)
		metadata[MetadataSequence] = strconv.FormatInt(sequence, 10)
		item.Metadata = metadata
		r.sessionSequences[sessionID] = sequence
	}
	return nil
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
	responses := make([]putResponse, len(writable))
	var wg sync.WaitGroup
	for i, binding := range writable {
		wg.Go(func() {
			responses[i] = runPut(ctx, binding, items)
		})
	}
	wg.Wait()
	return responses
}

// runPut executes one provider write under the binding's bulkhead and timeout,
// with the same slot-holding semantics as runSearch.
func runPut(ctx context.Context, binding ProviderBinding, items []MemoryItem) putResponse {
	providerCtx := ctx
	cancel := func() {}
	if binding.Timeout > 0 {
		providerCtx, cancel = context.WithTimeout(ctx, binding.Timeout)
	}
	defer cancel()
	select {
	case binding.putSlot <- struct{}{}:
	case <-providerCtx.Done():
		return putResponse{binding: binding, err: providerCtx.Err()}
	}
	providerResult := make(chan putResponse, 1)
	go func() {
		defer func() { <-binding.putSlot }()
		refs, err := putBatch(providerCtx, binding.Provider, items)
		providerResult <- putResponse{binding: binding, refs: refs, err: err}
	}()
	select {
	case result := <-providerResult:
		return result
	case <-providerCtx.Done():
		return putResponse{binding: binding, err: providerCtx.Err()}
	}
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

func applyPutPolicy(items []MemoryItem, policy PutPolicy, now time.Time) []MemoryItem {
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
				base = now
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

func recencyScore(createdAt time.Time, boost float64, now time.Time) float64 {
	if boost <= 0 || createdAt.IsZero() {
		return 0
	}
	age := now.Sub(createdAt)
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

func (r *Router) PreservesTurnBoundaries(provider string) bool {
	binding, ok := r.byName[strings.TrimSpace(provider)]
	if !ok {
		return false
	}
	capability, ok := binding.Provider.(TurnBoundaryProvider)
	return ok && capability.PreserveTurnBoundaries()
}

func dedupeKey(hit MemoryHit) string {
	if hit.Text != "" {
		provenance := hit.Provenance
		if provenance == (Provenance{}) {
			provenance = ProvenanceFromMetadata(hit.Metadata)
		}
		return "text:" + provenance.ScopeType + ":" + provenance.ScopeID + ":" + strings.Join(strings.Fields(strings.ToLower(hit.Text)), " ")
	}
	return "id:" + hit.Provider + ":" + hit.ID
}
