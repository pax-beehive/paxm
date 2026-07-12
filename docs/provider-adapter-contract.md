# Provider Adapter Contract

Every paxm provider adapter must satisfy the same boundary contract. The shared
test harness lives in `internal/adapters/contracttest`; SQLite, Mem0, Mem0
Cloud, MemOS, MemOS Cloud, Zep, and JSON-RPC each run it with a provider-specific fixture.

| Shared contract | SQLite | Mem0 | Mem0 Cloud | MemOS | MemOS Cloud | Zep | JSON-RPC |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Stable provider name | yes | yes | yes | yes | yes | yes | yes |
| Health semantics | yes | yes | yes | yes | yes | yes | yes |
| Write acknowledgement maps to provider/ref ID | yes | yes | yes | yes | receipt | yes | yes |
| Search returns provider/ID/text faithfully | yes | yes | yes | yes | yes | yes | yes |
| Context cancellation propagates | yes | yes | yes | yes | yes | yes | yes |

MemOS Cloud's OpenMem add API acknowledges ingestion without guaranteeing a
concrete memory ID. Paxm therefore returns a unique write receipt and does not
claim reliable per-memory cleanup for that API.

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
required fidelity and advertised batch/delete lifecycle capabilities.
