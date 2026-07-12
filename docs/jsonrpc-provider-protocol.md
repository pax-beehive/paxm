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

### `paxm.health`

Params are `{}`. Any successful JSON-RPC result means the provider is healthy.

### `paxm.put`

Params are one `MemoryItem`. Important fields are `id`, `text`, `metadata`,
`source`, `created_at`, `tier`, and `expires_at`. Unknown fields should be
ignored for forward compatibility. The result must contain a stable ref:

```json
{"ref":{"id":"memory-123"}}
```

### `paxm.search`

Params contain `text`, optional `limit`, `metadata`, and `tiers`. The result is:

```json
{"hits":[{"id":"memory-123","text":"...","relevance":0.92,"score":0.92,"metadata":{}}]}
```

`id` and `text` must faithfully identify stored content. Normalized scores
should be in `[0,1]`; backend-native values may use `raw_score` and
`raw_score_kind`. Metadata filters must not be silently discarded.

## Capability discovery and optional methods

`paxm.capabilities` takes `{}` and returns:

```json
{"put_batch":true,"delete":true}
```

The method is optional for legacy providers. `-32601` means no optional
capabilities are advertised.

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
exercised. Ranking quality, consolidation, latency, and result counts are not
adapter conformance requirements.

See [`examples/jsonrpc-provider`](../examples/jsonrpc-provider) for a complete
Go implementation.
