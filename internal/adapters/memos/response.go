package memos

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/memory"
)

func (p *Provider) hits(payload any, limit int) []memory.MemoryHit {
	items := memoryObjects(payload)
	hits := make([]memory.MemoryHit, 0, min(limit, len(items)))
	for _, item := range items {
		text := firstString(item, "memory_value", "memory", "text", "content")
		if text == "" {
			continue
		}
		id := firstString(item, "memory_id", "id", "node_id")
		if id == "" {
			id = p.newID()
		}
		score, scored := firstNumber(item, "relativity", "score", "similarity")
		if !scored {
			if metadata, ok := item["metadata"].(map[string]any); ok {
				score, scored = firstNumber(metadata, "relativity", "score", "similarity")
			}
		}
		kind := "memos_unscored"
		var raw *float64
		if scored {
			native := score
			score = normalize(score)
			raw = &native
			kind = "memos_relativity"
		} else {
			score = 1
		}
		hits = append(hits, memory.MemoryHit{Provider: p.name, ID: id, Text: text, Source: p.source(), Relevance: score, Score: score, RawScore: raw, RawScoreKind: kind, Metadata: stringMap(item["metadata"]), CreatedAt: firstTime(item, "update_time", "create_time", "created_at")})
		if len(hits) >= limit {
			break
		}
	}
	return hits
}

func (p *Provider) source() string {
	if p.dialect == cloud {
		return "memos-cloud"
	}
	return "memos"
}

func memoryObjects(value any) []map[string]any {
	root, _ := value.(map[string]any)
	if data, ok := root["data"]; ok {
		return memoryObjects(data)
	}
	for _, key := range []string{"memory_detail_list", "memories", "results"} {
		if list, ok := root[key].([]any); ok {
			return objectList(list)
		}
	}
	if buckets, ok := root["text_mem"].([]any); ok {
		var result []map[string]any
		for _, bucket := range objectList(buckets) {
			if list, ok := bucket["memories"].([]any); ok {
				result = append(result, objectList(list)...)
			} else {
				result = append(result, bucket)
			}
		}
		return result
	}
	if list, ok := value.([]any); ok {
		return objectList(list)
	}
	return nil
}

func objectList(values []any) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func firstID(value any) string {
	if item, ok := value.(map[string]any); ok {
		if id := firstString(item, "memory_id", "id", "message_id", "task_id"); id != "" {
			return id
		}
		for _, key := range []string{"data", "result", "results"} {
			if id := firstID(item[key]); id != "" {
				return id
			}
		}
	}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if id := firstID(item); id != "" {
				return id
			}
		}
	}
	return ""
}

func firstString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(item[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func firstNumber(item map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := item[key].(type) {
		case float64:
			return value, true
		case json.Number:
			parsed, err := value.Float64()
			return parsed, err == nil
		case string:
			parsed, err := strconv.ParseFloat(value, 64)
			return parsed, err == nil
		}
	}
	return 0, false
}

func normalize(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func stringMap(value any) map[string]string {
	item, _ := value.(map[string]any)
	if len(item) == 0 {
		return nil
	}
	result := make(map[string]string, len(item))
	for key, value := range item {
		result[key] = fmt.Sprint(value)
	}
	return result
}

func firstTime(item map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		value := firstString(item, key)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

func itemMetadata(item memory.MemoryItem) map[string]any {
	result := make(map[string]any, len(item.Metadata)+4)
	for key, value := range item.Metadata {
		result[key] = value
	}
	if item.Source != "" {
		result["paxm_source"] = item.Source
	}
	result["paxm_tier"] = string(memory.NormalizeTier(item.Tier))
	if item.ExpiresAt != nil {
		result["paxm_expires_at"] = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}
