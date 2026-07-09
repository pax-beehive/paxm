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
paxm [--config PATH] recall --query TEXT [--json]
paxm [--config PATH] remember --text TEXT
paxm [--config PATH] config doctor
```

`user_input` runs passive recall by rendering the configured hook recall
template into a query. It also renders the configured write template and appends
the result to the hook buffer.

`session_start` only appends a write item to the hook buffer.

`turn_end` appends a write item and flushes the buffer to the configured write
profile. The buffer is owned by a short-lived local Unix-socket daemon and lives
only in process memory. It is intentionally not durable.
