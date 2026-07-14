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
)

func ApplyProvenance(item MemoryItem, provenance Provenance) MemoryItem {
	metadata := cloneMetadata(item.Metadata)
	delete(metadata, MetadataUserID)
	delete(metadata, MetadataAgentID)
	delete(metadata, MetadataScopeType)
	delete(metadata, MetadataScopeID)
	setMetadata(metadata, MetadataUserID, provenance.UserID)
	setMetadata(metadata, MetadataAgentID, provenance.AgentID)
	setMetadata(metadata, MetadataScopeType, provenance.ScopeType)
	setMetadata(metadata, MetadataScopeID, provenance.ScopeID)
	item.Metadata = metadata
	item.Provenance = provenance
	return item
}

func ProvenanceFromMetadata(metadata map[string]string) Provenance {
	return Provenance{
		UserID: strings.TrimSpace(metadata[MetadataUserID]), AgentID: strings.TrimSpace(metadata[MetadataAgentID]),
		ScopeType: strings.TrimSpace(metadata[MetadataScopeType]), ScopeID: strings.TrimSpace(metadata[MetadataScopeID]),
	}
}

func WithoutProvenanceMetadata(metadata map[string]string) map[string]string {
	cleaned := cloneMetadata(metadata)
	delete(cleaned, MetadataUserID)
	delete(cleaned, MetadataAgentID)
	delete(cleaned, MetadataScopeType)
	delete(cleaned, MetadataScopeID)
	return cleaned
}

func setMetadata(metadata map[string]string, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = value
	}
}

func admitLongTermMemories(items []MemoryItem) []MemoryItem {
	admitted := append([]MemoryItem(nil), items...)
	now := time.Now().UTC()
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
	cloned := make(map[string]string, len(metadata)+4)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
