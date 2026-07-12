package capturequeue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/paxm/internal/facade"
	_ "modernc.org/sqlite"
)

// Options controls queue policy. Delivery behavior is intentionally added at
// this seam rather than exposed to hook callers.
type Options struct {
	MaxEpisodeAge       time.Duration
	RetryMin            time.Duration
	MaxAttempts         int
	Providers           func(profile string) []string
	ProviderConcurrency func(provider string) int
	Deliver             func(context.Context, string, Episode) (string, error)
	OnDelivery          func(DeliveryOutcome)
}

type Event struct {
	ID         string
	SessionKey string
	Terminal   bool
	Sequence   *int64
	Final      *int64
	Item       facade.IngestInput
}

type Receipt struct {
	EventID   string
	Sequence  int64
	Duplicate bool
}

type Stats struct {
	PendingEvents     int
	PendingEpisodes   int
	PendingDeliveries int
}

type Episode struct {
	ID         string               `json:"id"`
	SessionKey string               `json:"session_key"`
	Complete   bool                 `json:"complete"`
	Missing    []int64              `json:"missing_sequences,omitempty"`
	Integrity  []string             `json:"integrity_errors,omitempty"`
	Checksum   string               `json:"checksum"`
	Events     []facade.IngestInput `json:"events"`
	CapturedAt time.Time            `json:"captured_at"`
}

func (e Episode) IngestInput() facade.IngestInput {
	items := e.IngestInputs()
	if len(items) == 0 {
		return facade.IngestInput{}
	}
	return items[0]
}

func (e Episode) IngestInputs() []facade.IngestInput {
	type group struct {
		profile, tier, expires string
	}
	grouped := make(map[group][]facade.IngestInput)
	for _, item := range e.Events {
		expires := ""
		if item.ExpiresAt != nil {
			expires = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		key := group{profile: item.Profile, tier: string(item.Tier), expires: expires}
		grouped[key] = append(grouped[key], item)
	}
	keys := make([]group, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].profile != keys[j].profile {
			return keys[i].profile < keys[j].profile
		}
		if keys[i].tier != keys[j].tier {
			return keys[i].tier < keys[j].tier
		}
		return keys[i].expires < keys[j].expires
	})
	items := make([]facade.IngestInput, 0, len(keys))
	for _, key := range keys {
		items = append(items, e.ingestGroup(key.profile, grouped[key], len(keys) > 1))
	}
	return items
}

func (e Episode) ingestGroup(profile string, events []facade.IngestInput, multiple bool) facade.IngestInput {
	var texts []string
	var admissionText string
	metadata := map[string]string{
		"paxm_episode_id":       e.ID,
		"paxm_episode_complete": fmt.Sprintf("%t", e.Complete),
		"paxm_episode_events":   fmt.Sprintf("%d", len(e.Events)),
		"paxm_session_key":      e.SessionKey,
		"paxm_episode_checksum": e.Checksum,
	}
	if len(e.Missing) > 0 {
		missing := make([]string, 0, len(e.Missing))
		for _, sequence := range e.Missing {
			missing = append(missing, strconv.FormatInt(sequence, 10))
		}
		metadata["paxm_missing_sequences"] = strings.Join(missing, ",")
	}
	if len(e.Integrity) > 0 {
		metadata["paxm_integrity_errors"] = strings.Join(e.Integrity, ",")
	}
	var createdAt time.Time
	for _, item := range events {
		if text := strings.TrimSpace(item.Text); text != "" {
			texts = append(texts, text)
		}
		if admissionText == "" && strings.TrimSpace(item.AdmissionText) != "" {
			admissionText = item.AdmissionText
		}
		if createdAt.IsZero() || (!item.CreatedAt.IsZero() && item.CreatedAt.Before(createdAt)) {
			createdAt = item.CreatedAt
		}
		for key, value := range item.Metadata {
			if _, reserved := metadata[key]; !reserved {
				metadata[key] = value
			}
		}
	}
	id := e.ID
	if multiple {
		id += "_" + checksum([]byte(profile + "\x00" + string(events[0].Tier) + "\x00" + expiryString(events[0].ExpiresAt)))[:12]
	}
	return facade.IngestInput{
		ID:            id,
		Text:          strings.Join(texts, "\n\n"),
		AdmissionText: admissionText,
		Profile:       profile,
		Source:        "hook:episode",
		Metadata:      metadata,
		CreatedAt:     createdAt,
		Tier:          events[0].Tier,
		ExpiresAt:     events[0].ExpiresAt,
	}
}

func expiryString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

type RunResult struct {
	Delivered int
	Failed    int
	Dead      int
}

type deliveryClaim struct {
	episodeID    string
	provider     string
	episode      Episode
	attempts     int
	profiles     map[string]bool
	corruptErr   error
	captureTimes []time.Time
}

type deliveryResult struct {
	claim                    deliveryClaim
	ref                      string
	duration                 time.Duration
	passiveWriteLatencyTotal time.Duration
	passiveWriteSamples      int
	err                      error
}

type DeliveryOutcome struct {
	EpisodeID                string
	SessionKey               string
	Provider                 string
	Attempt                  int
	Ref                      string
	Duration                 time.Duration
	PassiveWriteLatencyTotal time.Duration
	PassiveWriteSamples      int
	Err                      error
	Dead                     bool
}

type Queue struct {
	db                 *sql.DB
	mu                 sync.Mutex
	opts               Options
	providerSemaphores map[string]chan struct{}
}

func Open(path string, opts Options) (*Queue, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("capture queue path is required")
	}
	if opts.MaxEpisodeAge <= 0 {
		opts.MaxEpisodeAge = time.Minute
	}
	if opts.RetryMin <= 0 {
		opts.RetryMin = time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 10
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	q := &Queue{db: db, opts: opts, providerSemaphores: make(map[string]chan struct{})}
	if err := q.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`UPDATE capture_deliveries SET state = 'retry', lease_until = '', next_attempt_at = '' WHERE state = 'delivering'`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return q, nil
}

func (q *Queue) Close() error { return q.db.Close() }

// RecoverDelivering makes claims retryable after an interrupted delivery
// cycle. Queue.Open performs the same recovery for process restarts; workers
// use this path when a database error aborts a cycle without restarting.
func (q *Queue) RecoverDelivering(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, err := q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'retry', lease_until = '', next_attempt_at = '' WHERE state = 'delivering'`)
	return err
}

func (q *Queue) Append(ctx context.Context, event Event) (Receipt, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if strings.TrimSpace(event.SessionKey) == "" {
		return Receipt{}, errors.New("capture event session key is required")
	}
	if strings.TrimSpace(event.Item.Text) == "" {
		return Receipt{}, errors.New("capture event text is required")
	}
	if event.ID == "" {
		event.ID = newID("evt")
	}
	capturedAt := time.Now().UTC()
	payload, err := marshalItem(event.Item)
	if err != nil {
		return Receipt{}, err
	}
	payloadHash := checksum(payload)
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingSequence int64
	var existingSession, existingHash string
	var existingSource, existingFinal sql.NullInt64
	var existingTerminal bool
	err = tx.QueryRowContext(ctx, `SELECT sequence, session_key, payload_hash, source_sequence, final_sequence, terminal FROM capture_events WHERE event_id = ?`, event.ID).Scan(&existingSequence, &existingSession, &existingHash, &existingSource, &existingFinal, &existingTerminal)
	if err == nil {
		if existingSession != event.SessionKey || existingHash != payloadHash || !sameSequence(existingSource, event.Sequence) || !sameSequence(existingFinal, event.Final) || existingTerminal != event.Terminal {
			return Receipt{}, fmt.Errorf("capture event %s conflicts with an existing event", event.ID)
		}
		return Receipt{EventID: event.ID, Sequence: existingSequence, Duplicate: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM capture_events WHERE session_key = ?`, event.SessionKey).Scan(&sequence); err != nil {
		return Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO capture_events(event_id, session_key, sequence, source_sequence, final_sequence, terminal, payload_json, payload_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, event.ID, event.SessionKey, sequence, nullableSequence(event.Sequence), nullableSequence(event.Final), event.Terminal, payload, payloadHash, capturedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Receipt{}, err
	}
	if event.Terminal {
		if err := q.sealSession(ctx, tx, event.SessionKey, event.Terminal); err != nil {
			return Receipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return Receipt{EventID: event.ID, Sequence: sequence}, nil
}

type episodeDraft struct {
	eventIDs            []string
	events              []facade.IngestInput
	first               int64
	last                int64
	sourceSequences     map[int64]int
	sourceMode          bool
	finalSequence       int64
	finalSequences      map[int64]bool
	sourceSequenceCount int
	eventHashes         []string
	capturedAt          time.Time
}

func (q *Queue) sealSession(ctx context.Context, tx *sql.Tx, sessionKey string, complete bool) error {
	draft, err := q.loadEpisodeDraft(ctx, tx, sessionKey)
	if err != nil {
		return err
	}
	if len(draft.events) == 0 {
		return nil
	}
	episode := draft.buildEpisode(sessionKey, complete)
	return q.persistEpisode(ctx, tx, episode, draft.eventIDs, draft.first, draft.last)
}

func (q *Queue) loadEpisodeDraft(ctx context.Context, tx *sql.Tx, sessionKey string) (episodeDraft, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT event_id, sequence, source_sequence, final_sequence, payload_json, payload_hash, created_at FROM capture_events
WHERE session_key = ? AND episode_id = '' ORDER BY sequence
`, sessionKey)
	if err != nil {
		return episodeDraft{}, err
	}
	defer func() { _ = rows.Close() }()
	draft := episodeDraft{
		sourceSequences: make(map[int64]int),
		finalSequences:  make(map[int64]bool),
	}
	for rows.Next() {
		var eventID, payload, payloadHash, createdAt string
		var sequence int64
		var sourceSequence, sourceFinal sql.NullInt64
		if err := rows.Scan(&eventID, &sequence, &sourceSequence, &sourceFinal, &payload, &payloadHash, &createdAt); err != nil {
			return episodeDraft{}, err
		}
		if checksum([]byte(payload)) != payloadHash {
			return episodeDraft{}, fmt.Errorf("capture event %s payload checksum mismatch", eventID)
		}
		item, err := unmarshalItem([]byte(payload))
		if err != nil {
			return episodeDraft{}, err
		}
		observedAt, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return episodeDraft{}, err
		}
		if draft.capturedAt.IsZero() || observedAt.Before(draft.capturedAt) {
			draft.capturedAt = observedAt
		}
		if draft.first == 0 {
			draft.first = sequence
		}
		draft.last = sequence
		if sourceSequence.Valid {
			draft.sourceMode = true
			draft.sourceSequenceCount++
			draft.sourceSequences[sourceSequence.Int64]++
		}
		if sourceFinal.Valid {
			draft.sourceMode = true
			draft.finalSequence = sourceFinal.Int64
			draft.finalSequences[sourceFinal.Int64] = true
		}
		draft.eventIDs = append(draft.eventIDs, eventID)
		draft.eventHashes = append(draft.eventHashes, payloadHash)
		draft.events = append(draft.events, item)
	}
	if err := rows.Err(); err != nil {
		return episodeDraft{}, err
	}
	return draft, nil
}

func (d episodeDraft) buildEpisode(sessionKey string, complete bool) Episode {
	missing, integrity := d.sequenceIntegrity(complete)
	if complete && d.sourceMode {
		complete = len(missing) == 0 && len(integrity) == 0
	}
	return Episode{ID: newID("ep"), SessionKey: sessionKey, Complete: complete, Missing: missing, Integrity: integrity, Checksum: checksum([]byte(strings.Join(d.eventHashes, "\n"))), Events: d.events, CapturedAt: d.capturedAt}
}

func (d episodeDraft) sequenceIntegrity(complete bool) ([]int64, []string) {
	if !complete || !d.sourceMode {
		return nil, nil
	}
	var missing []int64
	var integrity []string
	if len(d.finalSequences) != 1 || d.finalSequence <= 0 {
		integrity = append(integrity, "invalid_final_sequence")
	}
	if d.sourceSequenceCount != len(d.events) {
		integrity = append(integrity, "missing_source_sequence")
	}
	for sequence, count := range d.sourceSequences {
		if count > 1 {
			integrity = append(integrity, "duplicate_sequence:"+strconv.FormatInt(sequence, 10))
		}
		if d.finalSequence > 0 && sequence > d.finalSequence {
			integrity = append(integrity, "sequence_after_final:"+strconv.FormatInt(sequence, 10))
		}
	}
	if d.finalSequence > 0 {
		for sequence := int64(1); sequence <= d.finalSequence; sequence++ {
			if d.sourceSequences[sequence] == 0 {
				missing = append(missing, sequence)
			}
		}
	}
	return missing, integrity
}

func (q *Queue) persistEpisode(ctx context.Context, tx *sql.Tx, episode Episode, eventIDs []string, first, last int64) error {
	payload, err := json.Marshal(episode)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO capture_episodes(episode_id, session_key, first_sequence, last_sequence, complete, payload_json, payload_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, episode.ID, episode.SessionKey, first, last, episode.Complete, payload, checksum(payload), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := q.persistEpisodeRoutes(ctx, tx, episode.ID, q.episodeProviderRoutes(episode.Events)); err != nil {
		return err
	}
	return linkEpisodeEvents(ctx, tx, episode.ID, eventIDs)
}

func (q *Queue) episodeProviderRoutes(events []facade.IngestInput) map[string]map[string]bool {
	providers := make(map[string]map[string]bool)
	if q.opts.Providers == nil {
		return providers
	}
	for _, item := range events {
		for _, provider := range q.opts.Providers(item.Profile) {
			provider = strings.TrimSpace(provider)
			if provider == "" {
				continue
			}
			if providers[provider] == nil {
				providers[provider] = make(map[string]bool)
			}
			providers[provider][item.Profile] = true
		}
	}
	return providers
}

func (q *Queue) persistEpisodeRoutes(ctx context.Context, tx *sql.Tx, episodeID string, providers map[string]map[string]bool) error {
	for provider, profiles := range providers {
		profileNames := make([]string, 0, len(profiles))
		for profile := range profiles {
			profileNames = append(profileNames, profile)
		}
		sort.Strings(profileNames)
		profilesJSON, err := json.Marshal(profileNames)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO capture_deliveries(episode_id, provider, profiles_json) VALUES (?, ?, ?)`, episodeID, provider, profilesJSON); err != nil {
			return err
		}
	}
	return nil
}

func linkEpisodeEvents(ctx context.Context, tx *sql.Tx, episodeID string, eventIDs []string) error {
	for _, eventID := range eventIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE capture_events SET episode_id = ? WHERE event_id = ?`, episodeID, eventID); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) RunOnce(ctx context.Context) (RunResult, error) {
	q.mu.Lock()
	if q.opts.Deliver == nil {
		q.mu.Unlock()
		return RunResult{}, nil
	}
	claims, err := q.loadDeliveryClaims(ctx)
	if err != nil {
		q.mu.Unlock()
		return RunResult{}, err
	}
	touchedEpisodeIDs := deliveryEpisodeIDs(claims)
	claims, result, err := q.verifyDeliveryClaims(ctx, claims)
	if err != nil {
		q.mu.Unlock()
		return result, err
	}
	if err := q.markDelivering(ctx, claims); err != nil {
		q.mu.Unlock()
		return result, err
	}
	semaphores := q.deliverySemaphores(claims)
	q.mu.Unlock()

	outcomes := q.deliverClaims(ctx, claims, semaphores)
	result, err = q.persistDeliveryOutcomes(ctx, outcomes, result)
	if err != nil {
		return result, err
	}
	return result, q.refreshEpisodeStates(ctx, touchedEpisodeIDs)
}

func deliveryEpisodeIDs(claims []deliveryClaim) []string {
	seen := make(map[string]struct{}, len(claims))
	ids := make([]string, 0, len(claims))
	for _, claim := range claims {
		if _, ok := seen[claim.episodeID]; ok {
			continue
		}
		seen[claim.episodeID] = struct{}{}
		ids = append(ids, claim.episodeID)
	}
	return ids
}

func (q *Queue) loadDeliveryClaims(ctx context.Context) ([]deliveryClaim, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := q.db.QueryContext(ctx, `
SELECT d.episode_id, d.provider, d.profiles_json, e.payload_json, e.payload_hash, d.attempts
FROM capture_deliveries d JOIN capture_episodes e ON e.episode_id = d.episode_id
WHERE d.state IN ('pending', 'retry')
  AND (d.next_attempt_at = '' OR d.next_attempt_at <= ?)
  AND NOT EXISTS (
    SELECT 1 FROM capture_deliveries prior_d
    JOIN capture_episodes prior_e ON prior_e.episode_id = prior_d.episode_id
    WHERE prior_d.provider = d.provider
      AND prior_e.session_key = e.session_key
      AND prior_e.first_sequence < e.first_sequence
			AND prior_d.state NOT IN ('delivered', 'dead')
  )
ORDER BY e.created_at LIMIT 100
`, now)
	if err != nil {
		return nil, err
	}
	var claims []deliveryClaim
	for rows.Next() {
		var value deliveryClaim
		var profilesJSON, payload, payloadHash string
		if err := rows.Scan(&value.episodeID, &value.provider, &profilesJSON, &payload, &payloadHash, &value.attempts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if checksum([]byte(payload)) != payloadHash {
			value.episode = Episode{ID: value.episodeID}
			value.corruptErr = errors.New("capture episode envelope checksum mismatch")
			claims = append(claims, value)
			continue
		}
		if err := json.Unmarshal([]byte(payload), &value.episode); err != nil {
			value.episode = Episode{ID: value.episodeID}
			value.corruptErr = fmt.Errorf("capture episode payload is invalid: %w", err)
			claims = append(claims, value)
			continue
		}
		var profiles []string
		if err := json.Unmarshal([]byte(profilesJSON), &profiles); err != nil {
			value.corruptErr = fmt.Errorf("capture delivery route snapshot is invalid: %w", err)
			claims = append(claims, value)
			continue
		}
		value.profiles = make(map[string]bool, len(profiles))
		for _, profile := range profiles {
			value.profiles[profile] = true
		}
		claims = append(claims, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (q *Queue) verifyDeliveryClaims(ctx context.Context, claims []deliveryClaim) ([]deliveryClaim, RunResult, error) {
	verified := claims[:0]
	var result RunResult
	for _, claim := range claims {
		verifiedClaim, claimResult, keep, err := q.verifyDeliveryClaim(ctx, claim)
		if err != nil {
			return nil, result, err
		}
		result.Dead += claimResult.Dead
		if keep {
			verified = append(verified, verifiedClaim)
		}
	}
	return verified, result, nil
}

func (q *Queue) verifyDeliveryClaim(ctx context.Context, claim deliveryClaim) (deliveryClaim, RunResult, bool, error) {
	verifyErr := claim.corruptErr
	if verifyErr == nil {
		verifyErr = q.verifyEpisode(ctx, claim.episode)
	}
	if verifyErr != nil {
		if err := q.quarantineDelivery(ctx, claim, verifyErr); err != nil {
			return deliveryClaim{}, RunResult{}, false, err
		}
		if q.opts.OnDelivery != nil {
			q.opts.OnDelivery(DeliveryOutcome{EpisodeID: claim.episodeID, SessionKey: claim.episode.SessionKey, Provider: claim.provider, Attempt: claim.attempts + 1, Err: verifyErr, Dead: true})
		}
		return deliveryClaim{}, RunResult{Dead: 1}, false, nil
	}

	claim.captureTimes, verifyErr = q.episodeCaptureTimes(ctx, claim.episodeID)
	if verifyErr == nil && len(claim.captureTimes) != len(claim.episode.Events) {
		verifyErr = fmt.Errorf("capture episode %s capture timestamp count mismatch", claim.episodeID)
	}
	if verifyErr != nil {
		if err := q.quarantineDelivery(ctx, claim, verifyErr); err != nil {
			return deliveryClaim{}, RunResult{}, false, err
		}
		return deliveryClaim{}, RunResult{Dead: 1}, false, nil
	}
	filterDeliveryClaim(&claim)
	return claim, RunResult{}, true, nil
}

func (q *Queue) quarantineDelivery(ctx context.Context, claim deliveryClaim, reason error) error {
	_, err := q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'dead', attempts = attempts + 1, last_error = ? WHERE episode_id = ? AND provider = ?`, reason.Error(), claim.episodeID, claim.provider)
	return err
}

func filterDeliveryClaim(claim *deliveryClaim) {
	if len(claim.profiles) == 0 {
		return
	}
	filtered := claim.episode.Events[:0]
	filteredTimes := claim.captureTimes[:0]
	for index, item := range claim.episode.Events {
		if claim.profiles[item.Profile] {
			filtered = append(filtered, item)
			filteredTimes = append(filteredTimes, claim.captureTimes[index])
		}
	}
	claim.episode.Events = filtered
	claim.captureTimes = filteredTimes
}

func (q *Queue) markDelivering(ctx context.Context, claims []deliveryClaim) error {
	for _, claim := range claims {
		if _, err := q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'delivering', attempts = attempts + 1 WHERE episode_id = ? AND provider = ?`, claim.episodeID, claim.provider); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) deliverySemaphores(claims []deliveryClaim) map[string]chan struct{} {
	for _, value := range claims {
		if _, ok := q.providerSemaphores[value.provider]; ok {
			continue
		}
		concurrency := 1
		if q.opts.ProviderConcurrency != nil && q.opts.ProviderConcurrency(value.provider) > 0 {
			concurrency = q.opts.ProviderConcurrency(value.provider)
		}
		q.providerSemaphores[value.provider] = make(chan struct{}, concurrency)
	}
	semaphores := make(map[string]chan struct{}, len(q.providerSemaphores))
	for provider, semaphore := range q.providerSemaphores {
		semaphores[provider] = semaphore
	}
	return semaphores
}

func (q *Queue) deliverClaims(ctx context.Context, claims []deliveryClaim, semaphores map[string]chan struct{}) []deliveryResult {
	outcomes := make(chan deliveryResult, len(claims))
	for _, value := range claims {
		go func(value deliveryClaim) {
			semaphore := semaphores[value.provider]
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			started := time.Now()
			ref, err := q.opts.Deliver(ctx, value.provider, value.episode)
			completed := time.Now()
			latencyTotal, latencySamples := passiveWriteLatency(completed, value.captureTimes)
			outcomes <- deliveryResult{claim: value, ref: ref, duration: completed.Sub(started), passiveWriteLatencyTotal: latencyTotal, passiveWriteSamples: latencySamples, err: err}
		}(value)
	}
	results := make([]deliveryResult, 0, len(claims))
	for range claims {
		results = append(results, <-outcomes)
	}
	return results
}

func (q *Queue) persistDeliveryOutcomes(ctx context.Context, outcomes []deliveryResult, result RunResult) (RunResult, error) {
	for _, value := range outcomes {
		q.mu.Lock()
		var err error
		result, err = q.persistDeliveryOutcome(ctx, value, result)
		q.mu.Unlock()
		if err != nil {
			return result, err
		}
		if q.opts.OnDelivery != nil {
			q.opts.OnDelivery(DeliveryOutcome{
				EpisodeID:                value.claim.episodeID,
				SessionKey:               value.claim.episode.SessionKey,
				Provider:                 value.claim.provider,
				Attempt:                  value.claim.attempts + 1,
				Ref:                      value.ref,
				Duration:                 value.duration,
				PassiveWriteLatencyTotal: value.passiveWriteLatencyTotal,
				PassiveWriteSamples:      value.passiveWriteSamples,
				Err:                      value.err,
				Dead:                     value.err != nil && value.claim.attempts+1 >= q.opts.MaxAttempts,
			})
		}
	}
	return result, nil
}

func (q *Queue) persistDeliveryOutcome(ctx context.Context, value deliveryResult, result RunResult) (RunResult, error) {
	var err error
	if value.err == nil {
		_, err = q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'delivered', provider_ref = ?, delivered_at = ?, lease_until = '', last_error = '' WHERE episode_id = ? AND provider = ?`, value.ref, time.Now().UTC().Format(time.RFC3339Nano), value.claim.episodeID, value.claim.provider)
		result.Delivered++
	} else {
		attempt := value.claim.attempts + 1
		if attempt >= q.opts.MaxAttempts {
			_, err = q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'dead', next_attempt_at = '', lease_until = '', last_error = ? WHERE episode_id = ? AND provider = ?`, value.err.Error(), value.claim.episodeID, value.claim.provider)
			result.Dead++
		} else {
			shift := value.claim.attempts
			if shift > 6 {
				shift = 6
			}
			backoff := q.opts.RetryMin * time.Duration(1<<shift)
			_, err = q.db.ExecContext(ctx, `UPDATE capture_deliveries SET state = 'retry', next_attempt_at = ?, lease_until = '', last_error = ? WHERE episode_id = ? AND provider = ?`, time.Now().UTC().Add(backoff).Format(time.RFC3339Nano), value.err.Error(), value.claim.episodeID, value.claim.provider)
			result.Failed++
		}
	}
	return result, err
}

func (q *Queue) refreshEpisodeStates(ctx context.Context, episodeIDs []string) error {
	if len(episodeIDs) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	args := make([]any, len(episodeIDs))
	placeholders := make([]string, len(episodeIDs))
	for i, id := range episodeIDs {
		args[i] = id
		placeholders[i] = "?"
	}
	_, err := q.db.ExecContext(ctx, `UPDATE capture_episodes SET state = CASE
  WHEN EXISTS (SELECT 1 FROM capture_deliveries d WHERE d.episode_id = capture_episodes.episode_id AND d.state = 'dead') THEN 'dead'
  WHEN NOT EXISTS (SELECT 1 FROM capture_deliveries d WHERE d.episode_id = capture_episodes.episode_id AND d.state != 'delivered') THEN 'delivered'
  ELSE 'pending'
END WHERE episode_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func (q *Queue) SealExpired(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-q.opts.MaxEpisodeAge).Format(time.RFC3339Nano)
	return q.sealMatching(ctx, `SELECT DISTINCT session_key FROM capture_events WHERE episode_id = '' AND created_at <= ?`, cutoff)
}

func (q *Queue) SealAll(ctx context.Context) (int, error) {
	return q.sealMatching(ctx, `SELECT DISTINCT session_key FROM capture_events WHERE episode_id = ''`)
}

func (q *Queue) sealMatching(ctx context.Context, query string, args ...any) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	var sessionKeys []string
	for rows.Next() {
		var sessionKey string
		if err := rows.Scan(&sessionKey); err != nil {
			_ = rows.Close()
			return 0, err
		}
		sessionKeys = append(sessionKeys, sessionKey)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	sealed := 0
	for _, sessionKey := range sessionKeys {
		tx, err := q.db.BeginTx(ctx, nil)
		if err != nil {
			return sealed, err
		}
		if err := q.sealSession(ctx, tx, sessionKey, false); err != nil {
			_ = tx.Rollback()
			return sealed, err
		}
		if err := tx.Commit(); err != nil {
			return sealed, err
		}
		sealed++
	}
	return sealed, nil
}

func (q *Queue) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	queries := []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM capture_events WHERE episode_id = ''`, &stats.PendingEvents},
		{`SELECT COUNT(*) FROM capture_episodes WHERE state = 'pending'`, &stats.PendingEpisodes},
		{`SELECT COUNT(*) FROM capture_deliveries WHERE state IN ('pending', 'retry', 'delivering')`, &stats.PendingDeliveries},
	}
	for _, query := range queries {
		if err := q.db.QueryRowContext(ctx, query.query).Scan(query.dest); err != nil {
			return Stats{}, err
		}
	}
	return stats, nil
}

func (q *Queue) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS capture_events (
  event_id TEXT PRIMARY KEY,
  session_key TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  source_sequence INTEGER,
  final_sequence INTEGER,
  terminal INTEGER NOT NULL DEFAULT 0,
  payload_json TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  episode_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(session_key, sequence)
)`,
		`CREATE INDEX IF NOT EXISTS capture_events_pending ON capture_events(session_key, episode_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS capture_episodes (
  episode_id TEXT PRIMARY KEY,
  session_key TEXT NOT NULL,
  first_sequence INTEGER NOT NULL,
  last_sequence INTEGER NOT NULL,
  complete INTEGER NOT NULL,
  payload_json TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL,
  UNIQUE(session_key, first_sequence, last_sequence)
)`,
		`CREATE TABLE IF NOT EXISTS capture_deliveries (
  episode_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  profiles_json TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT '',
  lease_until TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  provider_ref TEXT NOT NULL DEFAULT '',
  delivered_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(episode_id, provider)
)`,
		`CREATE INDEX IF NOT EXISTS capture_deliveries_schedule ON capture_deliveries(state, next_attempt_at, provider, episode_id)`,
		`CREATE INDEX IF NOT EXISTS capture_episodes_schedule ON capture_episodes(session_key, first_sequence, episode_id)`,
	}
	for _, statement := range statements {
		if _, err := q.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("capture queue migrate: %w", err)
		}
	}
	for _, column := range []struct{ table, name, definition string }{
		{"capture_events", "source_sequence", "INTEGER"},
		{"capture_events", "final_sequence", "INTEGER"},
		{"capture_events", "payload_hash", "TEXT NOT NULL DEFAULT ''"},
		{"capture_episodes", "payload_hash", "TEXT NOT NULL DEFAULT ''"},
		{"capture_deliveries", "profiles_json", "TEXT NOT NULL DEFAULT '[]'"},
	} {
		if err := ensureQueueColumn(ctx, q.db, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	rows, err := q.db.QueryContext(ctx, `SELECT event_id, payload_json FROM capture_events WHERE payload_hash = ''`)
	if err != nil {
		return err
	}
	type unhashed struct{ id, payload string }
	var values []unhashed
	for rows.Next() {
		var value unhashed
		if err := rows.Scan(&value.id, &value.payload); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := q.db.ExecContext(ctx, `UPDATE capture_events SET payload_hash = ? WHERE event_id = ?`, checksum([]byte(value.payload)), value.id); err != nil {
			return err
		}
	}
	rows, err = q.db.QueryContext(ctx, `SELECT episode_id, payload_json FROM capture_episodes WHERE payload_hash = ''`)
	if err != nil {
		return err
	}
	values = nil
	for rows.Next() {
		var value unhashed
		if err := rows.Scan(&value.id, &value.payload); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := q.db.ExecContext(ctx, `UPDATE capture_episodes SET payload_hash = ? WHERE episode_id = ?`, checksum([]byte(value.payload)), value.id); err != nil {
			return err
		}
	}
	return nil
}

func ensureQueueColumn(ctx context.Context, db *sql.DB, table, name, definition string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if columnName == name {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+definition)
	return err
}

type persistedItem struct {
	Item          facade.IngestInput `json:"item"`
	AdmissionText string             `json:"admission_text,omitempty"`
}

func marshalItem(item facade.IngestInput) ([]byte, error) {
	return json.Marshal(persistedItem{Item: item, AdmissionText: item.AdmissionText})
}

func unmarshalItem(payload []byte) (facade.IngestInput, error) {
	var persisted persistedItem
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return facade.IngestInput{}, err
	}
	persisted.Item.AdmissionText = persisted.AdmissionText
	return persisted.Item, nil
}

func newID(prefix string) string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes)
}

func nullableSequence(sequence *int64) any {
	if sequence == nil {
		return nil
	}
	return *sequence
}

func sameSequence(stored sql.NullInt64, incoming *int64) bool {
	if incoming == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.Int64 == *incoming
}

func (q *Queue) verifyEpisode(ctx context.Context, episode Episode) error {
	rows, err := q.db.QueryContext(ctx, `SELECT payload_json, payload_hash FROM capture_events WHERE episode_id = ? ORDER BY sequence`, episode.ID)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var payload, hash string
		if err := rows.Scan(&payload, &hash); err != nil {
			_ = rows.Close()
			return err
		}
		if checksum([]byte(payload)) != hash {
			_ = rows.Close()
			return fmt.Errorf("capture episode %s event %d checksum mismatch", episode.ID, len(hashes)+1)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(hashes) != len(episode.Events) {
		return fmt.Errorf("capture episode %s event count mismatch", episode.ID)
	}
	if checksum([]byte(strings.Join(hashes, "\n"))) != episode.Checksum {
		return fmt.Errorf("capture episode %s checksum mismatch", episode.ID)
	}
	return nil
}

func (q *Queue) episodeCaptureTimes(ctx context.Context, episodeID string) ([]time.Time, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT created_at FROM capture_events WHERE episode_id = ? ORDER BY sequence`, episodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var captureTimes []time.Time
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		capturedAt, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, err
		}
		captureTimes = append(captureTimes, capturedAt)
	}
	return captureTimes, rows.Err()
}

func checksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func passiveWriteLatency(completed time.Time, captureTimes []time.Time) (time.Duration, int) {
	var total time.Duration
	var count int
	for _, capturedAt := range captureTimes {
		if capturedAt.IsZero() {
			continue
		}
		latency := completed.Sub(capturedAt)
		if latency < 0 {
			latency = 0
		}
		total += latency
		count++
	}
	if count == 0 {
		return 0, 0
	}
	return total, count
}
