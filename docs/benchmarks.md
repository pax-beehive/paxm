# SQLite adapter benchmarks

The SQLite adapter benchmarks exercise the public adapter `Put`, `PutBatch`,
and `Search` paths with payload sizes modeled after passive agent workloads.
All fixture text and databases are generated under `b.TempDir()` and deleted
after each benchmark. No benchmark dataset is stored in the repository.

## Running the benchmarks

The quick regression suite covers single writes from 1 KiB to 2 MiB, passive
batches, and short-memory corpora up to 100,000 items:

```bash
go test ./internal/adapters/sqlite -run '^$' \
  -bench 'BenchmarkSQLiteAdapter(Write|PassiveBatch|RecallShortCorpus)$' \
  -benchtime=300ms -count=3 -benchmem
```

The extended suite generates long-memory corpora up to 10,000 x 32 KiB,
roughly 320 MiB of source text. Run it intentionally:

```bash
go test ./internal/adapters/sqlite -run '^$' \
  -bench 'BenchmarkSQLiteAdapterRecallLongCorpus$' \
  -benchtime=200ms -count=1 -benchmem
```

## Reference results

Measured on 2026-07-11 using Go 1.26.4 on Apple M4 (`darwin/arm64`). Quick
suite values are medians of three runs. Extended values are single-run capacity
snapshots. These are regression references, not cross-machine guarantees.

| Write workload | Total payload | Median latency | Throughput |
| --- | ---: | ---: | ---: |
| Single item | 1 KiB | 0.715 ms | 1.43 MB/s |
| Single item | 8 KiB | 0.877 ms | 9.34 MB/s |
| Single item | 32 KiB | 1.212 ms | 27.04 MB/s |
| Single item | 128 KiB | 1.840 ms | 71.25 MB/s |
| Single item | 512 KiB | 4.263 ms | 122.98 MB/s |
| Single item | 1 MiB | 7.633 ms | 137.38 MB/s |
| Single item | 2 MiB | 14.307 ms | 146.59 MB/s |
| 10-item batch | 80 KiB | 2.290 ms | 35.77 MB/s |
| 10-item batch | 320 KiB | 6.442 ms | 50.87 MB/s |
| 10-item batch | 1.25 MiB | 12.359 ms | 106.06 MB/s |
| 20-item batch | 640 KiB | 11.318 ms | 57.91 MB/s |

| Recall corpus | Query | Latency | Bytes/op | Allocs/op |
| --- | --- | ---: | ---: | ---: |
| 1,000 x 256 B | hit | 0.471 ms | 14,540 | 359 |
| 1,000 x 256 B | miss | 0.457 ms | 12,677 | 332 |
| 10,000 x 256 B | hit | 0.462 ms | 14,515 | 359 |
| 10,000 x 256 B | miss | 0.434 ms | 12,673 | 332 |
| 100,000 x 256 B | hit | 0.540 ms | 14,506 | 359 |
| 100,000 x 256 B | miss | 0.485 ms | 12,672 | 332 |
| 1,000 x 8 KiB | hit | 0.528 ms | 40,176 | 359 |
| 1,000 x 8 KiB | miss | 0.429 ms | 13,148 | 332 |
| 1,000 x 32 KiB | hit | 0.610 ms | 120,760 | 359 |
| 1,000 x 32 KiB | miss | 0.432 ms | 13,162 | 332 |
| 10,000 x 8 KiB | hit | 0.504 ms | 40,010 | 359 |
| 10,000 x 8 KiB | miss | 0.504 ms | 13,136 | 332 |
| 10,000 x 32 KiB | hit | 0.613 ms | 120,665 | 359 |
| 10,000 x 32 KiB | miss | 0.435 ms | 13,125 | 332 |

These numbers include the adapter's current per-operation SQLite open and
close behavior. Payload construction, corpus seeding, and temporary database
creation are outside timed recall sections. Batch timing includes construction
of the `MemoryItem` slice and unique IDs because that work occurs on the passive
delivery path.
