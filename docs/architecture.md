# paxm Architecture

`paxm` exposes one CLI surface for agent memory while keeping provider setup,
hook installation, and recall policy in user-owned configuration.

## Layers

```text
cmd/paxm
  internal/cli          command parsing and interactive setup
  internal/mcp          stdio MCP server and memory tools
  internal/runtime      shared config, router, and facade loading
  internal/eval         versioned suites, isolated runtime runs, and metrics
  internal/facade       active recall, hook recall, and writes
  internal/memory       provider interface, routing, ranking, thresholds
  internal/adapters     provider registry
  internal/config       YAML config model and compatibility loading
  internal/telemetry    bounded local logs, metrics, and history summaries
```

The CLI, MCP server, and evaluation runner never talk to concrete providers
directly. They load config, build the provider registry/router, and call the
facade through the shared runtime seam. Evaluation cases use an isolated
temporary config and provider store rather than the user's normal config.

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

- `sqlite`: local SQLite storage with FTS5 candidate retrieval and normalized
  lexical relevance.
- `zep`: Zep Graph storage via `github.com/getzep/zep-go/v3`; writes text
  episodes and maps graph search results into memory hits.
- `mem0`: self-hosted Mem0 OSS REST storage; writes text through
  `POST /memories` and maps `POST /search` results into memory hits.
- `jsonrpc`: custom stdio plugin storage. The plugin implements the provider
  contract with JSON-RPC 2.0 methods while paxm keeps routing, thresholds, and
  ranking in core.

## Recall Profiles

A recall profile is the policy boundary for reads. It chooses:

- which enabled providers participate;
- whether each provider is required or best effort for that route;
- each provider route weight;
- max result count;
- relevance and final score thresholds, with optional provider-route overrides;
- ranking behavior;
- memory tiers (`stm`, `ltm`) to search.

`min_relevance` filters provider-normalized hits before cross-provider ranking.
`min_score` filters the final merged score after route weight and ranking boosts.
Provider-route thresholds override the profile default for that provider only.
The router asks each provider for a bounded candidate pool larger than the final
limit, then applies thresholds and cross-provider deduplication. Duplicate text
keeps the highest-scoring hit with deterministic tie-breaking, so concurrent
provider completion order cannot change the winner.

Passive hook recall should not use one policy for every turn. The default
`passive_initial` profile is used only for the first `user_input` observed for a
session and is intentionally looser, closer to a RAG warmup for project context.
The default `passive` profile is used for later `user_input` hooks and limits
results to 2 with higher relevance and score thresholds. Passive profiles read
`ltm` only by default.

Active recall is agent-driven. A skill or agent may run multiple explicit
`paxm recall` commands when an earlier result exposes a narrower lead, such as a
document title, issue id, symbol, command, error text, or decision keyword. Paxm
does not plan that query chain; it provides the recall surface and structured
scores. The agent should keep each follow-up query focused, stop after a small
number of hops, and verify current source when the remembered fact can drift.
The default active recall profile reads both `stm` and `ltm`, so short-lived
working memory can help active reasoning without being inserted by passive hooks.

Agents that support MCP can use `paxm mcp serve` instead of shelling out to the
CLI. The MCP server exposes `paxm_recall`, `paxm_remember`, `paxm_history`, and
`paxm_config_doctor` over stdio. It reuses the same facade and telemetry paths
as the CLI, so recall/write policy remains entirely in user-owned paxm config.
It intentionally does not expose setup, uninstall, hook installation, or
backfill execution as MCP tools.

## Write Profiles

A write profile is the policy boundary for writes. It chooses:

- which enabled providers receive writes;
- whether each provider is required or best effort for that write route;
- the memory tier assigned to the item;
- optional expiry for short-term memory.

Enabled providers can be used by multiple read and write profiles.
The built-in `stm` profile writes short-term working memory with a 24 hour
expiry. The built-in `ltm` profile writes durable long-term memory. Passive hook
writes default to `ltm`; active agents should use `stm` for task-local working
state and `ltm` only for durable facts. Configuration rejects unknown tier names,
requires every `stm` profile to have a positive expiry, and rejects expiry on
`ltm` profiles.

Before provider fan-out, LTM items without an explicit ID pass through a
deterministic admission module. It canonicalizes text case and whitespace,
includes `workspace` metadata in the identity scope when present, and assigns a
SHA-256 content ID plus lifecycle metadata. SQLite consolidates repeated IDs in
the same transaction, increments the occurrence count, keeps the earliest
`first_seen_at`, and advances `last_seen_at`. This works for both sequential
writes and duplicate items in one hook-buffer batch. STM items remain event-like
records, and explicit IDs remain authoritative so historical backfill identity
does not change.

`user_input` hooks separate identity from storage evidence: the stable prompt is
used as the admission text, while the rendered template including raw event data
remains the stored text. This prevents changing session IDs and other volatile
hook fields from producing a new LTM identity. Other hook events use their full
rendered text because collapsing different assistant outcomes under one prompt
would lose meaningful evidence.

The admission module only consolidates exact canonical matches. It does not use
an LLM, infer semantic equivalence, resolve conflicting facts, promote STM, or
supersede another memory. Remote adapters receive the stable ID and lifecycle
metadata, but the backing provider still decides whether repeated writes are
physically deduplicated.

## Agent Entries

An agent entry describes how an agent uses memory. It does not duplicate provider
configuration.

- `active_recall` is used by explicit `paxm recall --query ...` calls.
- `hooks.*.recall` is passive recall triggered by agent hooks.
- `hooks.*.write` is passive memory capture triggered by agent hooks.

Active recall and hook recall point at recall profiles. Hook writes point at
write profiles.

## Hook Behavior

V1 installs agent hook integrations through `paxm setup`. Both integrations use
`SessionStart`, `UserPromptSubmit`, and `Stop`. Claude Code additionally uses
`PostToolUse` and `PostToolUseFailure` for incremental tool capture:

```text
SessionStart      -> session_start
UserPromptSubmit  -> user_input
PostToolUse       -> tool_use (Claude Code)
PostToolUseFailure -> tool_failure (Claude Code)
Stop              -> turn_end
```

Each shim calls a hidden internal hook entrypoint. The public CLI surface stays:

```text
paxm [--config PATH] setup
paxm [--config PATH] recall --query TEXT [--limit N] [--json]
paxm [--config PATH] remember [--profile stm|ltm] --text TEXT
paxm [--config PATH] history [--days N] [--json]
paxm [--config PATH] logs [--tail N] [--follow] [--json]
paxm [--config PATH] backfill scan --agent AGENT [--before TIME]
paxm [--config PATH] backfill run --agent AGENT --provider NAME [--background]
paxm [--config PATH] backfill status --agent AGENT --provider NAME
paxm eval run [--suite PATH] [--json]
paxm [--config PATH] mcp serve
paxm [--config PATH] config doctor
```

`user_input` runs passive recall by rendering the configured hook recall
template into a query. The first `user_input` for a session can use the
configured `recall.initial` override, which typically points at the looser
`passive_initial` profile. Later `user_input` hooks use the normal strict
`recall` settings. It also renders the configured write template and appends the
result to the hook buffer. Before recall results are returned to the agent
context, the hook applies a second insertion policy such as minimum score,
maximum inserted items, and optional query-term overlap.

For Claude Code, `tool_use` and `tool_failure` record normalized tool name,
input, result, or error from `PostToolUse` and `PostToolUseFailure`, then append
them to the same buffer. For Codex, `turn_end` reads the
current local transcript and extracts function/custom tool calls and results,
including tools that do not emit `PostToolUse`. Both paths remove
thinking/reasoning recursively; transcript parsing is fail-open because Codex
documents that format as convenient but unstable.

The first-input decision is tracked in a bounded local state file under the paxm
hooks directory. The state stores only recent session keys and timestamps; it
does not store prompt text.

`session_start` only appends a write item to the hook buffer.

`turn_end` appends a write item and flushes the buffer to the configured write
profile. The buffer is owned by a short-lived local Unix-socket daemon and lives
only in process memory. It is intentionally not durable.

## Historical Session Backfill

Backfill readers normalize Codex, Claude Code, and Pi JSONL histories into
user/assistant turns. They discard system instructions, hidden reasoning, tool
traffic, sidechains, and attachments. Each normalized turn receives a
deterministic item ID, original timestamp, session ID, workspace, agent, and
`backfill:<agent>` source. Oversized turns are split into bounded deterministic
parts before entering the normal facade/router/provider write path.

The target is an exact enabled provider name rather than a write profile. This
keeps multiple Mem0 or custom provider instances unambiguous. Extraction rules
do not change: Mem0 and Zep can infer from the text, SQLite stores it directly,
and JSON-RPC plugins own their transformation behavior.

Foreground mode owns the worker and reports progress, item throughput, and ETA.
Background mode launches the same worker detached with stdout/stderr redirected
to its state log. A per-config, agent, and provider process lock rejects a
second live worker. A SQLite ledger records successful deterministic item IDs,
so later foreground or background invocations resume and skip completed items.
Status is written atomically for concurrent `paxm backfill status` reads.

Normal reruns are idempotent from paxm's perspective. Remote providers without
an idempotent client ID can still duplicate one item if the process dies after
the remote write succeeds but before the local ledger transaction commits.

Setup records an immutable first `passive_write_started_at` timestamp when an
agent first enables passive write. Backfill excludes turns at or after that cutoff. Older configs without a
recorded timestamp require an explicit `--before` value rather than guessing
and risking overlap with passive writes.

TTY setup uses terminal checkbox/select controls. Provider instances are
configured first, then selected agents are configured in stable order. Agent
setup changes only passive recall and write behavior; active skill installation
remains user-owned. A final summary is confirmed before config or integration
files are written. Non-TTY setup keeps the deterministic text prompt fallback.

Claude Code setup structurally merges these hooks into
`~/.claude/settings.json` and makes a one-time `.paxm.bak` backup. Existing hook
groups are preserved and paxm command handlers are deduplicated by command path.
The Claude `user_input` shim emits Markdown because stdout from
`UserPromptSubmit` is injected into Claude's context. The Claude `turn_end` shim
receives the native `Stop` event, including `last_assistant_message`, and uses it
as filtered write evidence without storing the full raw event or blocking Claude
from stopping.

## Hook Write Capture

Paxm does not run a shared memory-extraction or summarization step before
writing. Hook writes render the configured `hooks.*.write.template` into a text
payload, attach hook metadata, apply deterministic LTM admission when applicable,
and route that `MemoryItem` to the configured write profile. Built-in templates
use filtered `safe_text` rather than raw hook JSON, so long-term memory stores
user input, visible assistant output, and tool calls/results supplied in the
agent's normalized hook messages. Hidden thinking/reasoning and unrelated
runtime event structures remain excluded. Paxm does not reconstruct tool
traffic that an agent's hook payload does not expose. The provider decides what
to do with that text:

- `sqlite` stores the rendered text directly.
- `zep` writes the rendered text as a text episode and leaves graph extraction
  to Zep.
- `mem0` sends the rendered text as a single `role=user` message to the
  self-hosted Mem0 `/memories` API. Mem0 extraction follows the server default
  unless the provider `infer` config overrides it.
- `jsonrpc` passes the `MemoryItem` to the plugin. The plugin owns any
  extraction, summarization, filtering, or raw storage behavior.

This keeps paxm's write boundary simple: templates decide what evidence is sent,
write profiles decide which providers receive it and which memory tier it uses,
and providers own extraction. SQLite stores tier and expiry as columns. Zep,
Mem0, and JSON-RPC receive the same fields on `MemoryItem`; for remote results,
the core router also understands `paxm_tier` and `paxm_expires_at` metadata.

After a successful hook-buffer flush, paxm schedules a best-effort expired-memory
cleanup on a single daemon-owned worker. Cleanup is provider opt-in: SQLite
deletes a bounded batch of rows whose `expires_at` has passed, while providers
without cleanup support are skipped. Scheduling does not block the hook response,
and duplicate pending schedules are coalesced. Idle and shutdown paths drain
already scheduled cleanup before the daemon exits. Recall correctness does not
depend on cleanup because both SQLite and the core router filter expired hits
before returning them. The cleanup path is storage hygiene only and does not run
`VACUUM`.

Pi is integrated through Pi's extension system:

```text
before_agent_start -> user_input
message_end         -> visible user/assistant turn buffer
tool_execution_start -> tool args keyed by toolCallId
tool_execution_end   -> normalized tool call/result buffer
agent_end            -> turn_end
session_shutdown    -> best-effort final turn_end flush
```

Setup writes `~/.pi/agent/extensions/paxm-hook/index.ts`. The extension calls
the generated paxm `pi-user_input` shim and returns a `paxm-memory-recall`
message when the passive recall policy admits results. It also installs a
generated `pi-turn_end` shim. The extension keeps a small in-memory buffer of
visible messages and correlates Pi tool start/end events by `toolCallId`. It
sends the complete run to the `turn_end` hook at `agent_end` and flushes paxm's
hook buffer. `session_shutdown` makes one final best-effort flush for any
messages that did not observe an `agent_end`.

Pi lifecycle and `message_end` events use the runtime event bus rather than the
typed `before_agent_start` API surface, so this capture path is intentionally
best-effort. Hook write failures are recorded by paxm telemetry when possible
but do not block the Pi session.

`paxm uninstall` reconciles the opposite boundary. It best-effort flushes the
shared hook buffer, disables selected agents without erasing their hook choices,
removes exact paxm command handlers from Codex or Claude configuration, removes
Pi's paxm-owned extension directory, and deletes the selected shims. With no
`--agent`, it also asks the daemon to shut down. Provider config, memory data,
telemetry, active skills, backups, and the paxm executable remain user-owned and
are not removed.

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

`paxm logs` is the local raw-event view over this storage. Static tail reads
retained files from oldest backup through the active file and keeps the final N
events. Follow mode establishes its initial tail and active-file cursor while
holding the telemetry lock, then follows appends and reopens the active path when
rotation changes its file identity. It supports compact human output and JSONL.
The MCP interface keeps only aggregate `paxm_history`; raw logs remain local.

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
- publishes the generated archives, `SHA256SUMS`, and `install.sh` to the
  GitHub release.

`scripts/build-release.sh` is the single packaging path for both local releases
and GitHub Actions. It cross-compiles with `CGO_ENABLED=0`, injects the tag into
`paxm version`, and emits archives for:

- `darwin/amd64`
- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`
- `windows/arm64`

Release archives intentionally contain just the binary plus README. Runtime
config, API keys, hook installation, and local telemetry files remain user-owned
state created by `paxm setup` and normal CLI usage.

The installer is a small shell entrypoint over the same release artifacts. It
prints the PAX banner, detects the local `darwin` or `linux` platform, downloads
the matching archive from the latest or requested release, verifies
`SHA256SUMS`, and writes only the `paxm` binary into the selected install
directory.

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
