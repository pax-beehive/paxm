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

Provider-specific tests supplement this shared matrix with the request shapes
and response fields each backend actually supports. Coverage is intentionally
not represented as identical across providers: paxm does not invent tier,
expiry, raw-score, batch, or error capabilities that a backend does not expose.

External provider authors should use the normative
[`JSON-RPC Provider Protocol v1`](jsonrpc-provider-protocol.md) and run
`paxm eval provider jsonrpc --command ./provider`. The black-box kit verifies
required fidelity and advertised batch/delete lifecycle capabilities. A plugin
advertising `attribution:true` is also tested for exact origin/scope round-trip.
