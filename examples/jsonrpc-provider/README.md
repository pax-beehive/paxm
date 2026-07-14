# JSON-RPC provider example

This executable implements the paxm JSON-RPC provider protocol over stdio. Each
RPC request starts a fresh process, so the example persists data in a local JSON
file rather than process memory.

```bash
go build -o /tmp/paxm-sample-provider ./examples/jsonrpc-provider
PAXM_SAMPLE_PROVIDER_STORE=/tmp/paxm-sample-store.json \
  paxm eval provider jsonrpc --command /tmp/paxm-sample-provider
```

Configure it in paxm with `type: jsonrpc`, `transport: stdio`, the executable
path in `command`, and `PAXM_SAMPLE_PROVIDER_STORE` in the provider `env` map.
The JSON store is intentionally educational, not a production database.

The sample advertises `attribution:true`: it persists the provider-neutral
`origin` and `scope` objects and returns them unchanged on search. Run the
conformance command above after changing its storage mapping.
