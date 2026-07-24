package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	MetadataFingerprint = "paxm_fingerprint"
	MetadataOccurrences = "paxm_occurrences"
	MetadataFirstSeenAt = "paxm_first_seen_at"
	MetadataLastSeenAt  = "paxm_last_seen_at"
	MetadataUserID      = "paxm_user_id"
	MetadataAgentID     = "paxm_agent_id"
	MetadataScopeType   = "paxm_scope_type"
	MetadataScopeID     = "paxm_scope_id"
	MetadataSessionID   = "paxm_session_id"
	MetadataTurnID      = "paxm_turn_id"
	MetadataSequence    = "sequence"
)

func ApplyProvenance(item MemoryItem, provenance Provenance) MemoryItem {
	metadata := cloneMetadata(item.Metadata)
	delete(metadata, MetadataUserID)
	delete(metadata, MetadataAgentID)
	delete(metadata, MetadataScopeType)
	delete(metadata, MetadataScopeID)
	delete(metadata, MetadataSessionID)
	delete(metadata, MetadataTurnID)
	setMetadata(metadata, MetadataUserID, provenance.UserID)
	setMetadata(metadata, MetadataAgentID, provenance.AgentID)
	setMetadata(metadata, MetadataScopeType, provenance.ScopeType)
	setMetadata(metadata, MetadataScopeID, provenance.ScopeID)
	item.Metadata = metadata
	item.Origin.UserID = provenance.UserID
	item.Origin.AgentID = provenance.AgentID
	item.Scope = MemoryScope{Type: provenance.ScopeType, ID: provenance.ScopeID}
	item.Provenance = provenance
	return item
}

// PrepareProviderItem synchronizes trusted structured attribution with the
// metadata representation used by providers that only support string maps.
func PrepareProviderItem(item MemoryItem) MemoryItem {
	origin, scope := itemAttribution(item)
	if item.Turn != nil {
		if origin.SessionID == "" {
			origin.SessionID = strings.TrimSpace(item.Turn.SessionID)
		}
		if origin.TurnID == "" {
			origin.TurnID = strings.TrimSpace(item.Turn.TurnID)
		}
	}
	metadata := WithoutAttributionMetadata(item.Metadata)
	setMetadata(metadata, MetadataUserID, origin.UserID)
	setMetadata(metadata, MetadataAgentID, origin.AgentID)
	setMetadata(metadata, MetadataSessionID, origin.SessionID)
	setMetadata(metadata, MetadataTurnID, origin.TurnID)
	setMetadata(metadata, MetadataScopeType, scope.Type)
	setMetadata(metadata, MetadataScopeID, scope.ID)
	item.Metadata = metadata
	item.Origin = origin
	item.Scope = scope
	item.Provenance = Provenance{UserID: origin.UserID, AgentID: origin.AgentID, ScopeType: scope.Type, ScopeID: scope.ID}
	return item
}

// ApplyHitAttribution restores structured attribution from a provider hit.
// Structured fields win; legacy provenance and metadata are fallbacks.
func ApplyHitAttribution(hit MemoryHit) MemoryHit {
	origin := hit.Origin
	scope := hit.Scope
	if origin.UserID == "" {
		origin.UserID = strings.TrimSpace(hit.Provenance.UserID)
	}
	if origin.AgentID == "" {
		origin.AgentID = strings.TrimSpace(hit.Provenance.AgentID)
	}
	if scope.Type == "" {
		scope.Type = strings.TrimSpace(hit.Provenance.ScopeType)
	}
	if scope.ID == "" {
		scope.ID = strings.TrimSpace(hit.Provenance.ScopeID)
	}
	metadataOrigin, metadataScope := AttributionFromMetadata(hit.Metadata)
	if origin.UserID == "" {
		origin.UserID = metadataOrigin.UserID
	}
	if origin.AgentID == "" {
		origin.AgentID = metadataOrigin.AgentID
	}
	if origin.SessionID == "" {
		origin.SessionID = metadataOrigin.SessionID
	}
	if origin.TurnID == "" {
		origin.TurnID = metadataOrigin.TurnID
	}
	if scope.Type == "" {
		scope.Type = metadataScope.Type
	}
	if scope.ID == "" {
		scope.ID = metadataScope.ID
	}
	if scope.Type == "" || scope.ID == "" {
		scope = MemoryScope{Type: "unknown"}
	}
	hit.Origin = origin
	hit.Scope = scope
	hit.Provenance = Provenance{UserID: origin.UserID, AgentID: origin.AgentID, ScopeType: scope.Type, ScopeID: scope.ID}
	return hit
}

func AttributionFromMetadata(metadata map[string]string) (MemoryOrigin, MemoryScope) {
	return MemoryOrigin{
		UserID: strings.TrimSpace(metadata[MetadataUserID]), AgentID: strings.TrimSpace(metadata[MetadataAgentID]),
		SessionID: strings.TrimSpace(metadata[MetadataSessionID]), TurnID: strings.TrimSpace(metadata[MetadataTurnID]),
	}, MemoryScope{Type: strings.TrimSpace(metadata[MetadataScopeType]), ID: strings.TrimSpace(metadata[MetadataScopeID])}
}

func itemAttribution(item MemoryItem) (MemoryOrigin, MemoryScope) {
	origin := item.Origin
	scope := item.Scope
	if origin.UserID == "" {
		origin.UserID = strings.TrimSpace(item.Provenance.UserID)
	}
	if origin.AgentID == "" {
		origin.AgentID = strings.TrimSpace(item.Provenance.AgentID)
	}
	if scope.Type == "" {
		scope.Type = strings.TrimSpace(item.Provenance.ScopeType)
	}
	if scope.ID == "" {
		scope.ID = strings.TrimSpace(item.Provenance.ScopeID)
	}
	metadataOrigin, metadataScope := AttributionFromMetadata(item.Metadata)
	if origin.UserID == "" {
		origin.UserID = metadataOrigin.UserID
	}
	if origin.AgentID == "" {
		origin.AgentID = metadataOrigin.AgentID
	}
	if origin.SessionID == "" {
		origin.SessionID = metadataOrigin.SessionID
	}
	if origin.TurnID == "" {
		origin.TurnID = metadataOrigin.TurnID
	}
	if scope.Type == "" {
		scope.Type = metadataScope.Type
	}
	if scope.ID == "" {
		scope.ID = metadataScope.ID
	}
	return origin, scope
}

func ProvenanceFromMetadata(metadata map[string]string) Provenance {
	origin, scope := AttributionFromMetadata(metadata)
	provenance := Provenance{UserID: origin.UserID, AgentID: origin.AgentID, ScopeType: scope.Type, ScopeID: scope.ID}
	if provenance.ScopeType == "" || provenance.ScopeID == "" {
		provenance.ScopeType = "unknown"
		provenance.ScopeID = ""
	}
	return provenance
}

func WithoutProvenanceMetadata(metadata map[string]string) map[string]string {
	return WithoutAttributionMetadata(metadata)
}

func WithoutAttributionMetadata(metadata map[string]string) map[string]string {
	cleaned := cloneMetadata(metadata)
	delete(cleaned, MetadataUserID)
	delete(cleaned, MetadataAgentID)
	delete(cleaned, MetadataScopeType)
	delete(cleaned, MetadataScopeID)
	delete(cleaned, MetadataSessionID)
	delete(cleaned, MetadataTurnID)
	return cleaned
}

func setMetadata(metadata map[string]string, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = value
	}
}

func admitLongTermMemories(items []MemoryItem, now time.Time) []MemoryItem {
	admitted := append([]MemoryItem(nil), items...)
	for i := range admitted {
		item := admitted[i]
		if item.ID != "" || NormalizeTier(item.Tier) != TierLTM {
			continue
		}
		observedAt := item.CreatedAt
		if observedAt.IsZero() {
			observedAt = now
		}
		observedAt = observedAt.UTC()
		fingerprintText := item.AdmissionText
		if strings.TrimSpace(fingerprintText) == "" {
			fingerprintText = item.Text
		}
		fingerprint := longTermFingerprint(fingerprintText, item.Metadata)
		metadata := cloneMetadata(item.Metadata)
		metadata[MetadataFingerprint] = fingerprint
		metadata[MetadataOccurrences] = strconv.Itoa(1)
		metadata[MetadataFirstSeenAt] = observedAt.Format(time.RFC3339Nano)
		metadata[MetadataLastSeenAt] = observedAt.Format(time.RFC3339Nano)
		item.ID = "ltm_" + fingerprint
		item.CreatedAt = observedAt
		item.Metadata = metadata
		admitted[i] = item
	}
	return admitted
}

func longTermFingerprint(text string, metadata map[string]string) string {
	canonicalText := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
	workspace := strings.TrimSpace(metadata["workspace"])
	scopeType := strings.TrimSpace(metadata[MetadataScopeType])
	scopeID := strings.TrimSpace(metadata[MetadataScopeID])
	sum := sha256.Sum256([]byte(canonicalText + "\x00workspace=" + workspace + "\x00scope=" + scopeType + ":" + scopeID))
	return hex.EncodeToString(sum[:])
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata)+6)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
