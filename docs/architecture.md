# paxm Architecture

`paxm` exposes one CLI surface for agent memory while keeping provider setup,
hook installation, and recall policy in user-owned configuration.

## Layers

```text
cmd/paxm
  internal/cli          command parsing and interactive setup
  internal/facade       active recall, hook recall, and writes
  internal/memory       provider interface, routing, ranking, thresholds
  internal/adapters     provider registry
  internal/config       YAML config model and compatibility loading
  internal/telemetry    bounded local logs, metrics, and history summaries
```

The CLI never talks to concrete providers directly. It loads config, builds the
provider registry/router, and calls the facade.

## Provider Boundary

A memory provider is responsible for:

- connecting to one backing store or service;
- storing memory items;
- searching memory items;
- returning provider-local results with normalized relevance.

Provider relevance should be normalized to `[0, 1]` by the adapter. The router
can then compare hits from different providers without knowing provider-specific
score systems such as keyword ratios, vector distance, cosine similarity, or
vendor-specific ranks.

Provider configuration describes availability and connection details. It should
not decide whether a specific hook or active recall path reads from the provider.

Current provider adapters:

- `local`: local JSONL storage with keyword relevance.
- `zep`: Zep Graph storage via `github.com/getzep/zep-go/v3`; writes text
  episodes and maps graph search results into memory hits.

## Recall Profiles

A recall profile is the policy boundary for reads. It chooses:

- which enabled providers participate;
- whether each provider is required or best effort for that route;
- each provider route weight;
- max result count;
- relevance and final score thresholds;
- ranking behavior.

`min_relevance` filters provider-normalized hits before cross-provider ranking.
`min_score` filters the final merged score after route weight and ranking boosts.

Passive hook recall should use its own strict profile instead of reusing active
recall defaults. The default `passive` profile limits results to 2 and uses
higher relevance and score thresholds.

## Write Profiles

A write profile is the policy boundary for writes. It chooses:

- which enabled providers receive writes;
- whether each provider is required or best effort for that write route.

Enabled providers can be used by multiple read and write profiles.

## Agent Entries

An agent entry describes how an agent uses memory. It does not duplicate provider
configuration.

- `active_recall` is used by explicit `paxm recall --query ...` calls.
- `hooks.*.recall` is passive recall triggered by agent hooks.
- `hooks.*.write` is passive memory capture triggered by agent hooks.

Active recall and hook recall point at recall profiles. Hook writes point at
write profiles.

## Hook Behavior

V1 installs three Codex hooks through `paxm setup`:

```text
SessionStart      -> session_start
UserPromptSubmit  -> user_input
Stop              -> turn_end
```

Each shim calls a hidden internal hook entrypoint. The public CLI surface stays:

```text
paxm [--config PATH] setup
paxm [--config PATH] recall --query TEXT [--limit N] [--json]
paxm [--config PATH] remember --text TEXT
paxm [--config PATH] history [--days N] [--json]
paxm [--config PATH] config doctor
```

`user_input` runs passive recall by rendering the configured hook recall
template into a query. It also renders the configured write template and appends
the result to the hook buffer. Before recall results are returned to the agent
context, the hook applies a second insertion policy such as minimum score,
maximum inserted items, and optional query-term overlap.

`session_start` only appends a write item to the hook buffer.

`turn_end` appends a write item and flushes the buffer to the configured write
profile. The buffer is owned by a short-lived local Unix-socket daemon and lives
only in process memory. It is intentionally not durable.

## Local Telemetry

The CLI records local telemetry after recall, remember, hook recall, and hook
write-buffer operations. Telemetry is best effort: write failures are reported to
stderr but do not fail the memory operation.

Telemetry has two storage paths:

- a rolling JSONL event log for debugging recent behavior;
- a compact metrics JSON file for `paxm history`.

The event log is bounded by `max_event_file_bytes` and `max_event_files`.
Rotation renames the active file to `.1`, shifts older backups, and deletes the
oldest backup beyond the configured limit. Metrics are overwritten on update and
prune daily buckets according to `retention_days`, so aggregate history does not
grow without bound.

Default events avoid storing raw query or memory text. They include query length,
a query hash prefix, profile, hook event, agent target, hit/insert/write counts,
provider recall/write counts, provider hit/ref counts, provider error counts,
and duration.

## Release Pipeline

`paxm` releases are tag-driven. Pushing a `v*` tag runs
`.github/workflows/release.yml`, which:

- checks out the full git history so the tag name is available;
- installs the Go version from `go.mod`;
- runs `go test ./...`;
- runs `scripts/build-release.sh`;
- publishes the generated archives and `SHA256SUMS` to the GitHub release.

`scripts/build-release.sh` is the single packaging path for both local releases
and GitHub Actions. It cross-compiles with `CGO_ENABLED=0`, injects the tag into
`paxm version`, and emits archives for:

- `darwin/amd64`
- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`
- `windows/arm64`

Release artifacts are intentionally just the binary plus README. Runtime config,
API keys, hook installation, and local telemetry files remain user-owned state
created by `paxm setup` and normal CLI usage.

## Self Update

`paxm update` is a release-client path layered on top of GitHub releases. It is
not part of provider routing or memory behavior.

The updater:

- resolves the target release, either from `--version` or GitHub's latest
  release API;
- selects the asset matching the current `GOOS/GOARCH`;
- downloads the archive and `SHA256SUMS`;
- verifies the archive checksum before extraction;
- extracts the `paxm` binary and replaces the current executable, or
  `--install-path` when provided.

The updater intentionally does not modify paxm config, Codex hooks, local memory
files, or telemetry files. It only replaces the binary. On Windows, replacing a
running executable is not supported; users should pass `--install-path` and move
the binary after the process exits.
