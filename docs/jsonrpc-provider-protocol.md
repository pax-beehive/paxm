# paxm JSON-RPC Provider Protocol v1

The JSON-RPC provider boundary lets an external executable implement memory
storage without being compiled into paxm. The protocol is language-neutral and
uses JSON-RPC 2.0 over stdio.

## Process and transport contract

Paxm starts a fresh provider process for every request. It writes exactly one
newline-terminated request to stdin, closes stdin, reads one response from
stdout, and then terminates the process. Providers must persist state outside
the process. Diagnostic text belongs on stderr; stdout must contain only the
JSON-RPC response.

Requests and responses use string IDs. A response must repeat the request ID.
Paxm applies the configured timeout to process startup, I/O, and completion.

```json
{"jsonrpc":"2.0","id":"1","method":"paxm.health","params":{}}
{"jsonrpc":"2.0","id":"1","result":{"ok":true}}
```

Errors use the JSON-RPC error object. Return `-32601` for an unsupported method
and `-32602` for invalid parameters. Error details may be placed in `data`.

## Required methods

### Wire types

All object fields not listed as required are optional. Implementations MUST
ignore unknown fields. Timestamps are RFC 3339 strings (fractional seconds are
allowed). Metadata is an object whose keys and values are strings. Tier values
are `stm` or `ltm`.

| Type | Field | JSON type | Requirement |
| --- | --- | --- | --- |
| `MemoryItem` | `text` | string | required, non-empty |
| | `id` | string | optional caller-supplied identity |
| | `source` | string | optional |
| | `metadata` | object of string to string | optional |
| | `created_at` | RFC 3339 string | optional |
| | `tier` | `stm` or `ltm` | optional |
| | `expires_at` | RFC 3339 string | optional |
| | `origin` | `MemoryOrigin` object | optional trusted write attribution |
| | `scope` | `MemoryScope` object | optional write visibility boundary |
| | `provenance` | legacy `Provenance` object | optional compatibility input |
| `MemoryOrigin` | `user_id` | string | optional originating user |
| | `agent_id` | string | optional originating agent |
| | `session_id` | string | optional originating agent session |
| | `turn_id` | string | optional originating turn |
| `MemoryScope` | `type` | string | optional scope kind, such as `personal` or `team` |
| | `id` | string | required when `type` is present |
| `Provenance` | `user_id` | string | legacy; use `origin.user_id` |
| | `agent_id` | string | legacy; use `origin.agent_id` |
| | `scope_type` | string | legacy; use `scope.type` |
| | `scope_id` | string | legacy; use `scope.id` |
| `MemoryRef` | `id` | string | required, stable and non-empty |
| | `provider` | string | optional; paxm replaces it with configured name |
| `SearchQuery` | `text` | string | required |
| | `limit` | positive integer | optional |
| | `metadata` | object of string to string | optional exact-match filters |
| | `tiers` | array of `stm` or `ltm` | optional filters |
| `MemoryHit` | `id` | string | required |
| | `text` | string | required |
| | `relevance` | number in `[0,1]` | required |
| | `score` | number in `[0,1]` | required |
| | `metadata` | object of string to string | optional |
| | `source` | string | optional |
| | `created_at` | RFC 3339 string | optional |
| | `tier` | `stm` or `ltm` | optional |
| | `expires_at` | RFC 3339 string | optional |
| | `raw_score` | number | optional backend-native score |
| | `raw_score_kind` | string | required when `raw_score` is present |
| | `origin` | `MemoryOrigin` object | required when attribution is advertised and known |
| | `scope` | `MemoryScope` object | required when attribution is advertised and known |
| | `provenance` | legacy `Provenance` object | optional compatibility output |

### Origin, scope, and trust

`origin` answers "where did this memory come from?" `scope` answers "which
visibility boundary was assigned when it was written?" They are deliberately
separate: changing or omitting an origin must not silently change access.

The `session_id` in `origin` is the agent/runtime session that produced the
memory. It is not a provider ingestion session, Mem0 `run_id`, OpenViking
session, conversation ID, process ID, or JSON-RPC request ID. A plugin may map
those native identifiers internally, but must return the original paxm value.

Paxm supplies write attribution from trusted runtime configuration. Plugins
must store it as data, not treat it as authentication. Search authorization
must be evaluated separately by the configured paxm/provider policy; this
protocol does not turn attribution into an ACL. Values copied from model output
or caller-controlled search metadata are not trusted identity.

### `paxm.health`

Params are `{}`. Any successful JSON-RPC result means the provider is healthy.

### `paxm.put`

Params are one `MemoryItem`. Important fields are `id`, `text`, `metadata`,
`source`, `created_at`, `tier`, `expires_at`, `origin`, and `scope`. Unknown
fields should be ignored for forward compatibility. The result must contain a
stable ref:

```json
{"ref":{"id":"memory-123"}}
```

### `paxm.search`

Params contain `text`, optional `limit`, `metadata`, and `tiers`. The result is:

```json
{"hits":[{"id":"memory-123","text":"...","relevance":0.92,"score":0.92,"metadata":{}}]}
```

`id` and `text` must faithfully identify stored content. Adapter-native scores
should be in `[0,1]`; backend-native values may use `raw_score` and
`raw_score_kind`. Paxm preserves `relevance` and `score` semantics, then derives
the same internal query-local ranking signal used for built-in providers before
cross-provider ordering. Calibration does not change the JSON-RPC contract.
Metadata filters must not be silently discarded.

## Capability discovery and optional methods

`paxm.capabilities` takes `{}` and returns:

```json
{"put_batch":true,"delete":true,"attribution":true}
```

The method is optional for legacy providers. `-32601` means no optional
capabilities are advertised.

`attribution:true` means the provider persists `origin` and `scope` on
`paxm.put` and `paxm.putBatch`, then returns the same values on matching
`paxm.search` hits. It is a fidelity promise, not an authorization claim. Do
not advertise it if the backend drops, rewrites, synthesizes, or cannot attach
attribution to individual hits. Legacy providers that omit the field continue
to work; paxm treats their attribution fidelity as unknown and skips the
attribution conformance check.

### `paxm.putBatch`

Params are `{"items":[MemoryItem,...]}` and the result is
`{"refs":[MemoryRef,...]}` in input order. On `-32601`, paxm falls back to
repeated `paxm.put` calls. Providers advertising `put_batch:true` must support
the method.

### `paxm.delete`

Params are a `MemoryRef`, such as `{"provider":"plugin","id":"memory-123"}`.
A successful response may be `{"deleted":true}`. Providers advertising
`delete:true` must make the ref unsearchable after success. This enables safe
eval cleanup when whole-scope deletion is unavailable.

## Conformance

Run the black-box kit against an executable:

```bash
paxm eval provider jsonrpc --command ./my-provider --arg value --json
```

Required checks cover health, write acknowledgement, stable ref IDs, and
faithful search mapping. Advertised batch and delete capabilities are also
exercised. When `attribution:true`, the kit writes distinct user, agent,
session, turn, and scope values and requires an exact round-trip. Ranking
quality, consolidation, latency, and result counts are not adapter conformance
requirements.

## Compatibility summary

- Existing v1 plugins remain valid because every new field is optional and
  implementations must ignore unknown fields.
- Existing `provenance` is accepted as a fallback, but cannot carry session or
  turn identity.
- New plugins should implement `origin` and `scope`, then advertise
  `attribution:true` only after the conformance command passes.
- Returning no attribution is safer than inventing attribution from a
  provider-native session or run identifier.

See [`examples/jsonrpc-provider`](../examples/jsonrpc-provider) for a complete
Go implementation.
