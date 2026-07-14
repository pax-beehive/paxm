# ADR 0001: Separate memory origin from visibility scope

Status: accepted

## Context

Paxm's legacy `provenance` object combines the writing user and agent with the
memory's visibility scope. It cannot represent the originating session or
turn. Provider adapters also differ in whether they preserve this information,
and provider-native session or run identifiers do not necessarily identify an
agent session.

Agent integrations can tell an agent its current user, agent, and session at
session bootstrap. That current identity does not describe the origin of every
recalled memory and cannot replace per-memory attribution.

## Decision

Provider-neutral memory items and hits carry:

- `origin`: `user_id`, `agent_id`, `session_id`, and `turn_id`;
- `scope`: a visibility `type` and `id`.

The router and attribution-capable adapters mirror these fields to canonical
`paxm_*` metadata when a backend only supports string metadata. Search results
restore the structured fields. The legacy `provenance` object remains as a
compatibility fallback.

Attribution is stored data, not authentication. Recall authorization uses
trusted runtime identity and must not trust model-supplied metadata. A
provider-native session, run, or conversation ID must not be substituted for
the original paxm agent session ID.

JSON-RPC providers explicitly advertise `attribution:true` only when they can
round-trip origin and scope for individual hits. Conformance tests enforce that
promise. Providers that cannot do so return unknown attribution instead of
synthesizing it.

## Consequences

- Existing providers and JSON-RPC plugins remain compatible with the legacy
  fields and optional new fields.
- SQLite, Mem0, Mem0 Cloud, MemOS, MemOS Cloud, and Zep can preserve origin and
  scope through metadata mapping.
- OpenViking currently cannot associate extracted search hits with paxm
  metadata, so its attribution remains unknown.
- Session bootstrap can avoid repeating current identity in every recall
  rendering, while each returned memory still carries its own origin.
