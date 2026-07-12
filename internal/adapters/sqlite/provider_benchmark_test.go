package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/memory"
)

func BenchmarkSQLiteAdapterWrite(b *testing.B) {
	for _, payloadBytes := range []int{1024, 8 << 10, 32 << 10, 128 << 10, 512 << 10, 1 << 20, 2 << 20} {
		b.Run(benchmarkSizeName("payload", payloadBytes), func(b *testing.B) {
			provider := benchmarkSQLiteProvider(b)
			text := benchmarkPayload(payloadBytes, "single-write-marker")
			benchmarkSingleWrites(b, provider, text)
		})
	}
}

func BenchmarkSQLiteAdapterPassiveBatch(b *testing.B) {
	for _, dataset := range []struct {
		items        int
		payloadBytes int
	}{
		{items: 10, payloadBytes: 8 << 10},
		{items: 10, payloadBytes: 32 << 10},
		{items: 10, payloadBytes: 128 << 10},
		{items: 20, payloadBytes: 32 << 10},
	} {
		name := fmt.Sprintf("items_%d/%s", dataset.items, benchmarkSizeName("payload", dataset.payloadBytes))
		b.Run(name, func(b *testing.B) {
			provider := benchmarkSQLiteProvider(b)
			texts := make([]string, dataset.items)
			for i := range texts {
				texts[i] = benchmarkPayload(dataset.payloadBytes, fmt.Sprintf("batch-marker-%02d", i))
			}
			benchmarkBatchWrites(b, provider, texts)
		})
	}
}

func BenchmarkSQLiteAdapterRecallShortCorpus(b *testing.B) {
	for _, corpusSize := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("items_%d", corpusSize), func(b *testing.B) {
			provider := benchmarkSQLiteProvider(b)
			seedBenchmarkCorpus(b, provider, corpusSize, 256)
			benchmarkRecallQueries(b, provider, corpusSize)
		})
	}
}

func BenchmarkSQLiteAdapterRecallLongCorpus(b *testing.B) {
	for _, dataset := range []struct {
		items        int
		payloadBytes int
	}{
		{items: 1000, payloadBytes: 8 << 10},
		{items: 1000, payloadBytes: 32 << 10},
		{items: 10000, payloadBytes: 8 << 10},
		{items: 10000, payloadBytes: 32 << 10},
	} {
		name := fmt.Sprintf("items_%d/%s", dataset.items, benchmarkSizeName("payload", dataset.payloadBytes))
		b.Run(name, func(b *testing.B) {
			provider := benchmarkSQLiteProvider(b)
			seedBenchmarkCorpus(b, provider, dataset.items, dataset.payloadBytes)
			benchmarkRecallQueries(b, provider, dataset.items)
		})
	}
}

func benchmarkSingleWrites(b *testing.B, provider *Provider, text string) {
	b.Helper()
	if _, err := provider.Put(context.Background(), memory.MemoryItem{ID: "warmup", Text: text, Source: "benchmark"}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := provider.Put(context.Background(), memory.MemoryItem{
			ID: fmt.Sprintf("write-%d", i), Text: text, Source: "benchmark",
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkBatchWrites(b *testing.B, provider *Provider, texts []string) {
	b.Helper()
	payloadBytes := 0
	for _, text := range texts {
		payloadBytes += len(text)
	}
	b.ReportAllocs()
	b.SetBytes(int64(payloadBytes))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		items := make([]memory.MemoryItem, len(texts))
		for i, text := range texts {
			items[i] = memory.MemoryItem{
				ID: fmt.Sprintf("batch-%d-%d", iteration, i), Text: text, Source: "benchmark",
			}
		}
		if _, err := provider.PutBatch(context.Background(), items); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(texts)), "items/op")
}

func benchmarkRecallQueries(b *testing.B, provider *Provider, corpusSize int) {
	b.Helper()
	queries := []struct {
		name string
		text string
	}{
		{name: "hit", text: fmt.Sprintf("benchmarker%06d", corpusSize/2)},
		{name: "miss", text: "absentmarkerbenchmark"},
	}
	for _, query := range queries {
		b.Run(query.name, func(b *testing.B) {
			search := memory.SearchQuery{Text: query.text, Limit: 5}
			if _, err := provider.Search(context.Background(), search); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := provider.Search(context.Background(), search); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(corpusSize), "corpus_items")
		})
	}
}

func benchmarkSQLiteProvider(b *testing.B) *Provider {
	b.Helper()
	provider, err := New("sqlite", filepath.Join(b.TempDir(), "benchmark.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	return provider
}

func seedBenchmarkCorpus(b *testing.B, provider *Provider, size, payloadBytes int) {
	b.Helper()
	items := make([]memory.MemoryItem, size)
	for i := range items {
		marker := fmt.Sprintf("benchmarker%06d", i)
		items[i] = memory.MemoryItem{
			ID:     fmt.Sprintf("seed-%06d", i),
			Text:   benchmarkPayload(payloadBytes, marker),
			Source: "benchmark",
		}
	}
	if _, err := provider.PutBatch(context.Background(), items); err != nil {
		b.Fatal(err)
	}
}

func benchmarkPayload(size int, marker string) string {
	prefix := "benchmark adapter memory " + marker + " "
	if size <= len(prefix) {
		return prefix[:size]
	}
	return prefix + strings.Repeat("x", size-len(prefix))
}

func benchmarkSizeName(prefix string, size int) string {
	switch {
	case size%(1<<20) == 0:
		return fmt.Sprintf("%s_%dMiB", prefix, size/(1<<20))
	case size%(1<<10) == 0:
		return fmt.Sprintf("%s_%dKiB", prefix, size/(1<<10))
	default:
		return fmt.Sprintf("%s_%dB", prefix, size)
	}
}
