# Provider Adapter Contract

Every paxm provider adapter must satisfy the same boundary contract. The shared
test harness lives in `internal/adapters/contracttest`; SQLite, Mem0, Mem0
Cloud, MemOS, MemOS Cloud, OpenViking, Zep, and JSON-RPC each run it with a
provider-specific fixture.

| Shared contract | SQLite | Mem0 | Mem0 Cloud | MemOS | MemOS Cloud | OpenViking | Zep | JSON-RPC |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Stable provider name | yes | yes | yes | yes | yes | yes | yes | yes |
| Health semantics | yes | yes | yes | yes | yes | yes | yes | yes |
| Write acknowledgement maps to provider/ref ID | yes | yes | yes | yes | receipt | receipt | yes | yes |
| Search returns provider/ID/text faithfully | yes | yes | yes | yes | yes | yes | yes | yes |
| Origin/scope metadata can round-trip | yes | yes | yes | yes | yes | no | yes | capability |
| Context cancellation propagates | yes | yes | yes | yes | yes | yes | yes | yes |

MemOS Cloud's OpenMem add API acknowledges ingestion without guaranteeing a
concrete memory ID. Paxm therefore returns a unique write receipt and does not
claim reliable per-memory cleanup for that API.

OpenViking similarly returns an asynchronous extraction task for a committed
session rather than one concrete memory ID. Paxm returns that task ID as the
write receipt and does not advertise per-memory cleanup. Its current API also
does not return paxm metadata on extracted memories, so OpenViking hits have
unknown origin and scope. The OpenViking ingestion session ID is provider-owned
and MUST NOT be presented as the originating agent session ID.

## Attribution contract

Paxm separates two concepts that older integrations combined as provenance:

- `origin` identifies the user, agent, session, and turn that produced a
  memory.
- `scope` identifies the visibility boundary assigned to that memory.

Adapters that support attribution must preserve both values across write and
search. Providers backed by string metadata receive the canonical keys
`paxm_user_id`, `paxm_agent_id`, `paxm_session_id`, `paxm_turn_id`,
`paxm_scope_type`, and `paxm_scope_id`. Adapters restore structured `origin`
and `scope` from those keys when reading results.

Attribution describes a stored memory; it is not proof of the current caller's
identity and it does not grant access. Callers must derive identity from trusted
runtime/session context and apply authorization independently. An agent must
not be able to widen recall by supplying these metadata keys.

This attribution change does not add a new cross-scope ACL or caller-derived
filter to `SearchQuery`. Existing recall-profile and provider-native policy
remain the authorization boundary until a separate trusted recall-principal
contract is introduced.

The legacy `provenance` object remains readable for compatibility. New
integrations should emit `origin` and `scope`; paxm prefers those structured
fields and only falls back to legacy provenance or canonical metadata.

The contract deliberately does not require equal ranking, semantic recall,
consolidation, latency, or result counts. Those are provider capabilities, not
paxm adapter correctness.

## Score semantics

Every adapter must expose `relevance` and `score` as higher-is-better values in
the `[0,1]` range before the router applies thresholds, weights, recency, or
ranking. `raw_score` remains the untouched backend value, and
`raw_score_kind` identifies its meaning.

Mem0 and Mem0 Cloud deployments configure this direction with
`providers.<name>.score_semantics`:

- `similarity` is the backward-compatible default for scores where larger is
  more relevant.
- `distance` is for pgvector cosine distance in `[0,2]`; paxm converts it to
  `1 - distance/2`, so smaller distance becomes larger relevance.

The adapter must not infer direction from whether a response field is called
`score`, `similarity`, or `relevance`: different Mem0 deployments and vector
stores use those names for different semantics. Invalid values fail config
validation. A distance response keeps `raw_score_kind: mem0_distance` or
`mem0_cloud_distance`; the normalized value is the only value passed into
router thresholds and hook insertion.

Recall diagnostics retain the provider's `raw_score_kinds`, `candidate_count`,
and `eligible_count` in the existing provider-recall telemetry details. This
makes a score-direction or threshold problem visible through existing logs and
JSON diagnostics without adding a new public command.

## Search filters versus runtime metadata

`SearchQuery.Metadata` carries runtime and diagnostic context (hook session
ids, event sources, recall phases). Providers must never translate it into
store-native filter criteria: hook payloads change per session, so metadata
filters silently exclude every previously written memory. Only
`SearchQuery.Filters`, which callers set explicitly, may become provider-side
filters. The Mem0 and Mem0 Cloud adapters follow this rule; both still send
the configured `user_id`, `agent_id`, and `run_id` entity scope from provider
configuration on every request.

`SearchQuery.RuntimeContext`, when present, is separate trusted hook context.
For Codex and Claude Code it is created from installed command flags plus the
hook process working directory before stdin is decoded. Adapters may use its
workspace for explicit local scope policy, but must not replace it with values
from `Metadata`, query text, or model output. Active and non-hook searches omit
the field. Hook payload decoding cannot populate the typed field; integrations
without a trusted host constructor omit it even when their raw payload contains
a same-named object.

Passive `MemoryItem.RuntimeContext` uses the same trusted structure. When the
capture queue combines several lifecycle events into one episode item, it keeps
the last contributing hook event; target and workspace remain stable because
the queue partitions episodes by the trusted session key.

## Mem0 search scope payload compatibility

The self-hosted Mem0 adapter owns the placement of provider entity scope in
`POST /search`. Every request must carry the configured non-empty `user_id`,
`agent_id`, and `run_id` values either inside `filters` or as top-level fields;
the router and hook layers do not rewrite them.

`providers.<name>.search_scope_payload` defines the contract:

- `filters` sends entity IDs only inside `filters`.
- `top_level` sends entity IDs only at the request top level while retaining
  non-scope metadata filters inside `filters`.
- `auto` is the backward-compatible default. It tries `filters`, retries once
  with `top_level` only for a recognized missing-scope response from `/search`,
  and caches a successful top-level fallback for that provider instance.

The fallback must not trigger for authentication failures, timeouts, malformed
queries, embedding failures, vector-store failures, or arbitrary 5xx responses.
If the fallback itself fails, its error is returned. The adapter does not infer
the working shape from OpenAPI field presence because Mem0 0.1.117 advertises a
`filters` field while its runtime still requires a top-level entity ID.

Provider-specific tests supplement this shared matrix with the request shapes
and response fields each backend actually supports. Coverage is intentionally
not represented as identical across providers: paxm does not invent tier,
expiry, raw-score, batch, or error capabilities that a backend does not expose.

External provider authors should use the normative
[`JSON-RPC Provider Protocol v1`](jsonrpc-provider-protocol.md) and run
`paxm eval provider jsonrpc --command ./provider`. The black-box kit verifies
required fidelity and advertised batch/delete lifecycle capabilities. A plugin
advertising `attribution:true` is also tested for exact origin/scope round-trip.
