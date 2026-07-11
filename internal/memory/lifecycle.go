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
)

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
	sum := sha256.Sum256([]byte(canonicalText + "\x00workspace=" + workspace))
	return hex.EncodeToString(sum[:])
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata)+4)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
