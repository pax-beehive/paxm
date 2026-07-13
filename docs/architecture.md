# paxm Architecture

`paxm` exposes one CLI surface for agent memory while keeping provider setup,
hook installation, and recall policy in user-owned configuration.

## Layers

```text
cmd/paxm
  internal/cli          thin command adapters grouped by audience
  internal/tools        agent-facing recall and remember interface
  internal/capture      passive lifecycle and durable capture workflow
  internal/mcp          stdio MCP server and memory tools
  internal/runtime      shared config, router, tools, and capture loading
  internal/eval         versioned suites, isolated runtime runs, and metrics
  internal/facade       compatibility implementation behind tools/capture
  internal/memory       provider interface, routing, ranking, thresholds
  internal/adapters     provider registry
  internal/config       YAML config model and compatibility loading
  internal/telemetry    bounded local logs, metrics, and history summaries
```

The CLI no longer imports the facade. Active CLI commands and MCP are adapters
over `runtime.Tools`; passive hooks are adapters over `runtime.Capture`.
`capture.Runtime` owns write-item policy, durable append, sequence, seal/flush,
and shutdown ordering. Provider diagnostics and evals may call adapter seams
directly because their purpose is to test below the application interfaces.

Top-level commands have an explicit audience:

- operator: setup, uninstall, config, history, logs, backfill, eval, update;
- agent tools: recall, remember, MCP;
- internal transport: hook, hook daemon, and hook control commands.

The macOS application is expected to replace operator CLI as the primary human
interface, not as the only recovery and automation adapter. Config inspection,
provider health, observation, batch operations, and cleanup already enter the
typed `operator.Service`; setup/install and binary update remain platform
adapters that should be moved behind operator application services as Desktop
is implemented. The application must never parse CLI text. Agent tools intentionally
cannot install hooks, mutate credentials, update paxm, or change routing.

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
- `mem0-cloud`: managed Mem0 Platform storage; encapsulates Token auth,
  asynchronous v3 writes, event polling, v3 search, and eval-scope cleanup.
- `memos`: self-hosted MemOS product API storage, scoped by user and memory
  cube. Its adapter owns `/product/add`, `/product/search`, and deletion shapes.
- `memos-cloud`: managed MemOS OpenMem API storage. Its separate dialect owns
  Token auth and `/add/message` plus `/search/memory`; provider details do not
  escape into the router, facade, CLI, MCP, or agent integrations.
- `jsonrpc`: custom stdio plugin storage. The plugin implements the provider
  contract with JSON-RPC 2.0 methods while paxm keeps routing, thresholds, and
  ranking in core.

SQLite retrieval is a deep module under `internal/adapters/sqlite/retrieval`.
It owns lexical analysis, candidate SQL, scoring, and result ordering behind one
`Search` operation using retrieval-local request and hit types. The SQLite
provider maps those types to the shared memory contract; the router, facade,
tools, CLI, and MCP do not depend on retrieval plans or FTS details. This seam
keeps future retrieval changes local and allows the module to move without
changing agent-facing interfaces.

Candidate retrieval combines FTS5 with a bounded, deterministic lexical
analyzer for camel/snake identifiers, paths, versions, error codes, CJK
substrings, conservative morphology, and a small product alias vocabulary. It
does not call an embedding model or perform open-ended semantic expansion.
FTS5 remains the fast path. The rg-like text scan runs only when analysis is
needed and FTS5 cannot fill the requested top-K with full matches; its all-term
predicate prevents partial-match noise from consuming the bounded ranking pool.
Retrieval then makes its reasoning explicit with three internal stages: exact
phrase, strict all-term, and relaxed partial. Only the earliest non-empty stage
is returned. Candidate lists from FTS5 and lexical fallback are combined with
reciprocal-rank fusion; concise evidence density and provider rank break ties
inside the selected stage. An internal trace records branch counts and the
selected stage without changing `memory.Provider`, facade, CLI, or MCP types.
When a query carries `metadata.workspace`, SQLite excludes rows owned by a
different workspace in SQL before scoring; unscoped memories remain visible as
shared memories for compatibility with the provider contract.

SQLite also bounds long recall results inside this retrieval module. Memories
shorter than 1,200 bytes are returned unchanged. For longer results, SQLite
selects query-bearing source segments plus adjacent context, preserves a
session timestamp when present, and prioritizes explicit date or duration
evidence for temporal questions. The selected text is extractive: it never
generates or paraphrases memory content and does not call an LLM or embedding
model. Internal defaults cap the combined top-K context at 8,000 bytes and an
individual hit at 2,400 bytes. These are SQLite implementation defaults rather
than public recall-profile settings; other providers are not processed by this
path. Excerpted hits retain their ID, source, scores, and ordering and add
`sqlite_excerpted` plus `sqlite_original_bytes` metadata for diagnosis.

## Recall Profiles

A recall profile is the policy boundary for reads. It chooses:

- which enabled providers participate;
- whether each provider is required or best effort for that route;
- each provider route weight;
- max result count;
- relevance and final score thresholds, with optional provider-route overrides;
- ranking behavior;
- memory tiers (`stm`, `ltm`) to search.

Adapters normalize native relevance to `[0,1]`, but those values are not assumed
to be distribution-compatible across providers. For each query, the router
sorts each provider's candidates by native relevance and derives a separate
internal ranking score as `normalized relevance / provider rank²`. Squared
reciprocal rank limits a flat-score provider from occupying the merged result set, while
the absolute relevance term ensures weak evidence is never promoted above its
adapter score. This is deterministic score normalization, not a claim that
vendor scores are calibrated probabilities. The original backend score and
adapter-normalized relevance remain on the hit for diagnostics and offline eval.
The ranking score stays inside the memory router; provider, facade, CLI, MCP,
and JSON-RPC interfaces retain their existing score fields and semantics.

`min_relevance` filters adapter-normalized relevance and `min_score` filters its
weighted/recency-adjusted public score, preserving their existing meanings.
Surviving hits are ordered by the internal calibrated ranking score.
Provider-route thresholds override the profile default for that provider only.
The router asks each provider for a bounded candidate pool larger than the final
limit, then applies thresholds and cross-provider deduplication. Duplicate text
keeps the highest-scoring hit with deterministic tie-breaking, so concurrent
provider completion order cannot change the winner. This calibration seam is
provider-agnostic and therefore also applies to custom JSON-RPC adapters.

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
CLI. The MCP server exposes only `paxm_recall` and `paxm_remember` over stdio.
It reuses the same least-privilege tools and telemetry paths as the CLI, so
recall/write policy remains entirely in user-owned paxm config. History,
provider diagnostics, setup, uninstall, hook installation, routing changes,
and backfill remain operator capabilities and are not MCP tools.

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
paxm eval run [--suite PATH] [--gate quality|adapter|none] [--json] [--compare RESULT.json] [--budget BUDGET.json] [--output RESULT.json]
paxm eval run locomo --dataset PATH --agent NAME --model PROVIDER/MODEL --provider NAME (--max-questions N | --all) [--arms control,passive,active] [--json] [--output RESULT.json]
paxm eval retrieval locomo --dataset PATH --provider NAME [--limit N] [--settle DURATION] [--keep-memory] [--json] [--output RESULT.json]
paxm eval provider jsonrpc --command PATH [--arg VALUE] [--timeout DURATION] [--json]
paxm eval cleanup (--run RUN_ID | --stale) [--manifest-dir PATH]
paxm [--config PATH] mcp serve
paxm [--config PATH] config doctor
```

Evaluation suites share the production runtime through `runtime.Tools`,
`runtime.Operator`, and `runtime.Capture`. Retrieval cases can
seed known memories before active or passive recall. Conversation-write cases
instead keep normalized user/assistant history separate from the normalized
hook-event messages rendered through `HookWriteItem`, persist the result through
`IngestBatch`, and issue a later recall against a seeded harmful distractor.
When an agent hook supplies complete turn history, the scenario explicitly
includes that history in the rendered event rather than duplicating it into an
assistant or tool field.
Their reports add capture-quality metrics, result count, write and recall
latency totals, returned recall-content bytes, and metadata survival checks
without introducing a second hook or retrieval implementation.

The primary LoCoMo runner evaluates a real agent in fresh control, passive, and
active sessions. Passive uses the agent's lifecycle hook and active uses the
same paxm MCP server exposed to users. It reports deterministic normalized
answer F1 and memory lift rather than claiming compatibility with the official
LLM-judge accuracy. Direct evidence Recall@K remains under `eval retrieval` as
a diagnostic for explaining agent failures.

Both modes give each conversation a provider-specific isolated scope and an
atomic manifest of created refs. SQLite databases are disposable, Mem0 uses a
unique run scope, and Zep uses a unique graph. Cleanup is attempted on both
success and failure; unsupported remote providers fail closed unless the
caller explicitly chooses `--keep-memory`. Stale manifests make interrupted
remote runs recoverable with `paxm eval cleanup`.

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

`session_start` appends a write event to the durable capture queue.

The queue is a SQLite WAL-backed event log partitioned by agent session. Hook
callers acknowledge after the event transaction commits; they do not wait for a
memory provider. `turn_end` seals only its own session's pending events into a
structured episode. Events with source sequence metadata are checked for gaps,
and every event payload plus the assembled episode carries a SHA-256 checksum.
Sessions that never emit `turn_end` are sealed as incomplete after the configured
maximum episode age.

Each episode creates one independent delivery per write-profile provider. A
background worker delivers different providers concurrently, preserves episode
order within each provider/session partition, and records provider-specific ACK,
retry, error, and reference state. SQLite defaults to one delivery worker while
network providers default to four. A stable episode ID makes retries idempotent
where the provider honors supplied IDs. Deliveries left in progress by a daemon
crash return to retry state when the queue reopens.

The Unix-socket daemon is single-instance per config directory. Its lock is
acquired before stale socket cleanup, preventing simultaneous hook cold starts
from unlinking another live daemon's socket. Queue state survives daemon and
machine restarts in `hooks/capture.sqlite` by default.

## Historical Session Backfill

Backfill readers normalize Codex, Claude Code, and Pi JSONL histories into
user/assistant turns. They discard system instructions, hidden reasoning, tool
traffic, sidechains, and attachments. Each normalized turn receives a
deterministic item ID, original timestamp, session ID, workspace, agent, and
`backfill:<agent>` source. Oversized turns are split into bounded deterministic
parts before entering the normal operator/router/provider write path.

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

`paxm uninstall` reconciles the opposite boundary. It best-effort seals and
delivers the durable capture queue, disables selected agents without erasing their hook choices,
removes exact paxm command handlers from Codex or Claude configuration, removes
Pi's paxm-owned extension directory, and deletes the selected shims. With no
`--agent`, it also asks the daemon to shut down. Provider config, memory data,
telemetry, active skills, backups, and the paxm executable remain user-owned and
are not removed.

## Local Telemetry

The CLI records local telemetry after recall, remember, hook recall, durable
capture, and provider delivery operations. Telemetry is best effort: write
failures are reported to stderr but do not fail the memory operation.

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
and duration. Successful passive deliveries separately record provider call
duration and average per-message latency from durable capture to provider ACK.
`paxm history` aggregates both values by provider without including failed
attempts in the averages.

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
  `--install-path` when provided;
- after a successful in-place install, asks the existing hook daemon to seal
  pending capture state durably, then shut down. The updater waits for both the
  socket and daemon lock to disappear; the next real hook starts the new binary
  and resumes delivery. A shutdown failure is reported as a warning and does not
  roll back a successfully installed executable.

The updater intentionally does not modify paxm config, Codex hooks, local memory
files, or telemetry files. `--check` never touches the daemon. On Windows,
replacing a running executable is not supported; users should pass
`--install-path` and move the binary after the process exits. Installing to an
alternate path does not stop the daemon for the currently running executable.
