# paxm YAML Config

Default config path for new installs:

```text
~/.config/paxm/config.yaml
```

`paxm` can still load legacy JSON configs for compatibility, but setup writes
YAML unless an explicit `.json` path is provided.

## Shape

```yaml
version: 1

providers:
  sqlite:
    type: sqlite
    enabled: true
    path: ~/.local/share/paxm/memory.sqlite

  zep:
    type: zep
    enabled: false
    api_key: "plain-text-zep-api-key"
    user_id: todd
    search_scope: episodes

  mem0:
    type: mem0
    enabled: false
    base_url: http://localhost:8888
    api_key: "plain-text-mem0-api-key"
    user_id: todd

  mem0_cloud:
    type: mem0-cloud
    enabled: false
    base_url: https://api.mem0.ai
    api_key: "plain-text-mem0-cloud-api-key"
    user_id: todd
    infer: false

  memos:
    type: memos
    enabled: false
    base_url: http://localhost:8000
    user_id: todd
    mem_cube_id: personal
    search_mode: fast

  memos_cloud:
    type: memos-cloud
    enabled: false
    base_url: https://memos.memtensor.cn/api/openmem/v1
    api_key: "plain-text-memos-cloud-api-key"
    user_id: todd
    agent_id: opencode

  jsonrpc:
    type: jsonrpc
    enabled: false
    transport: stdio
    command: /opt/paxm/plugins/private-memory
    args: ["--config", "/etc/private-memory.yaml"]
    timeout: 30s

recall_profiles:
  default:
    providers:
      - name: sqlite
        required: true
        weight: 1.0
    max_results: 3
    thresholds:
      min_relevance: 0.25
      min_score: 0.25
    ranking:
      type: weighted_relevance
      recency_boost: 0
    tiers: [stm, ltm]

  passive:
    providers:
      - name: sqlite
        required: true
        weight: 1.0
        timeout: 250ms
    max_results: 2
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
    ranking:
      type: weighted_relevance
      recency_boost: 0
    tiers: [ltm]

  passive_initial:
    providers:
      - name: sqlite
        required: true
        weight: 1.0
        timeout: 250ms
    max_results: 5
    thresholds:
      min_relevance: 0.35
      min_score: 0.35
    ranking:
      type: weighted_relevance
      recency_boost: 0
    tiers: [ltm]

write_profiles:
  default:
    tier: ltm
    providers:
      - name: sqlite
        required: true
        timeout: 30s
  stm:
    tier: stm
    expires_after: 24h
    providers:
      - name: sqlite
        required: true
        timeout: 30s
  ltm:
    tier: ltm
    providers:
      - name: sqlite
        required: true
        timeout: 30s

agents:
  claude:
    enabled: false
    active_recall:
      enabled: true
      profile: default
      output: markdown
    hooks:
      session_start:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: session_start
          buffer:
            enabled: true
            flush_count: 10

      user_input:
        recall:
          enabled: true
          profile: passive
          query_template: "{{ .prompt }}"
          max_results: 2
          timeout_extra: 100ms
          output: markdown
          insertion:
            min_score: 0.8
            max_items: 2
            require_query_terms: true
          initial:
            enabled: true
            profile: passive_initial
            max_results: 5
            insertion:
              min_score: 0.35
              max_items: 5
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: user_input
          buffer:
            enabled: true
            flush_count: 10

      tool_use:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: tool_use
          buffer:
            enabled: true
            flush_count: 10

      tool_failure:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: tool_failure
          buffer:
            enabled: true
            flush_count: 10

      turn_end:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: turn_end
          buffer:
            enabled: true
            flush: true
            flush_count: 10

  codex:
    enabled: true
    active_recall:
      enabled: true
      profile: default
      output: markdown
    hooks:
      session_start:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: session_start
          buffer:
            enabled: true
            flush_count: 10

      user_input:
        recall:
          enabled: true
          profile: passive
          query_template: "{{ .prompt }}"
          max_results: 2
          output: markdown
          insertion:
            min_score: 0.8
            max_items: 2
            require_query_terms: true
          initial:
            enabled: true
            profile: passive_initial
            max_results: 5
            insertion:
              min_score: 0.35
              max_items: 5
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: user_input
          buffer:
            enabled: true
            flush_count: 10

      turn_end:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: turn_end
          buffer:
            enabled: true
            flush: true
            flush_count: 10

  pi:
    enabled: false
    active_recall:
      enabled: true
      profile: default
      output: markdown
    hooks:
      user_input:
        recall:
          enabled: false
          profile: passive
          query_template: "{{ .prompt }}"
          max_results: 2
          output: markdown
          insertion:
            min_score: 0.8
            max_items: 2
            require_query_terms: true
          initial:
            enabled: true
            profile: passive_initial
            max_results: 5
            insertion:
              min_score: 0.35
              max_items: 5
      turn_end:
        write:
          enabled: true
          profile: ltm
          template: |
            {{ .safe_text }}
          mode: turn_end
          buffer:
            enabled: true
            flush: true
            flush_count: 10

telemetry:
  enabled: true
  dir: ~/.local/state/paxm
  events_file: events.jsonl
  metrics_file: metrics.json
  max_event_file_bytes: 1048576
  max_event_files: 3
  retention_days: 30
  capture_query_preview: false
  query_preview_chars: 80
```

## Providers

`providers` declares provider instances and connection details. The map key is
the provider instance name, not the adapter type. Multiple instances can share
one `type` as long as each instance has a unique key:

```yaml
providers:
  mem0_personal:
    type: mem0
    enabled: true
    base_url: http://localhost:8888
    user_id: todd

  mem0_team:
    type: mem0
    enabled: true
    base_url: https://mem0.internal.example
    api_key: "plain-text-mem0-api-key"
    agent_id: pax-team

  corp_memory:
    type: jsonrpc
    enabled: true
    transport: stdio
    command: /opt/paxm/plugins/corp-memory
```

Fields:

- `type`: adapter type, such as `sqlite`, `zep`, `mem0`, `mem0-cloud`, `memos`, `memos-cloud`, or `jsonrpc`.
- `enabled`: whether this provider can be used by profiles.
- `path`: local SQLite provider database path.
- `api_key`: optional plain-text API key for remote providers.
- `base_url`: optional remote provider API base URL override.
- `transport`: JSON-RPC plugin transport. V1 supports `stdio`.
- `command`: JSON-RPC plugin executable path.
- `args`: optional JSON-RPC plugin command arguments.
- `env`: optional environment variables for JSON-RPC plugin commands.
- `timeout`: optional JSON-RPC plugin call timeout, such as `30s`.
- `user_id`: Zep user graph target, or Mem0 user scope.
- `agent_id`: Mem0 agent scope.
- `run_id`: Mem0 run scope.
- `graph_id`: Zep named graph target.
- `mem_cube_id`: required memory cube for self-hosted MemOS.
- `search_mode`: self-hosted MemOS retrieval mode: `fast`, `fine`, or `mixture`.
- `search_scope`: Zep graph search scope. Supported values are `episodes`,
  `edges`, `nodes`, `observations`, `thread_summaries`, and `auto`.
- `max_characters`: optional Zep auto-scope context character limit.
- `source_description`: optional Zep source description for writes.
- `infer`: optional Mem0 write flag. Omit it to use the server default.

V1 ships with `sqlite`, `zep`, `mem0`, `mem0-cloud`, `memos`, `memos-cloud`, and `jsonrpc` provider adapters. Zep
requires `api_key` and exactly one of `user_id` or `graph_id`. If setup is
configured for a Zep user graph, it idempotently creates the configured
`user_id` when the user does not already exist.

Mem0 is intended for the self-hosted OSS REST server. Configure `base_url`
without a `/v1` prefix, for example `http://localhost:8888`, and set at least
one scope for paxm to use with `user_id`, `agent_id`, or `run_id`. Programmatic
auth uses `api_key` as an `X-API-Key` header; leave it blank only for local Mem0
deployments that intentionally run with auth disabled.

Mem0 Cloud uses the separate `mem0-cloud` type. It defaults to
`https://api.mem0.ai`, requires `api_key`, authenticates with `Authorization:
Token`, and encapsulates the platform's asynchronous v3 add and search APIs.
It uses the same scope and `infer` fields as the self-hosted adapter, but defaults
`infer` to `false` so agent episodes are stored verbatim with predictable write
latency and lexical recall. Explicit `infer: true` enables platform extraction
and may require a write-route timeout above the default `30s`. After a successful
asynchronous event, paxm retries the metadata lookup briefly to tolerate delayed
read visibility. Eval runs force `infer: false` and add an isolated `run_id` so
their writes can be removed.

MemOS self-hosted uses the product REST server (normally
`http://localhost:8000`) and requires both `user_id` and `mem_cube_id`. The
optional key is sent as a Bearer token. `search_mode` defaults to `fast`; use
`fine` or `mixture` only when the deployment supports their extra work. A
single write may extract several memories while the shared provider contract
returns one ref, so eval runs require explicit `--keep-memory` rather than
claiming incomplete cleanup.

MemOS Cloud is deliberately a separate `memos-cloud` dialect. It defaults to
the OpenMem endpoint `https://memos.memtensor.cn/api/openmem/v1`, requires an
API key sent as `Authorization: Token`, and scopes data by required `user_id`
plus optional `agent_id`. The OpenMem write response does not guarantee a
deletable memory ID, so destructive eval cleanup is unavailable; paid evals
must opt into `--keep-memory` explicitly.

JSON-RPC providers are custom plugin commands. Paxm invokes the command over
stdio with one JSON-RPC 2.0 request per provider operation. The command should
read a single request from stdin, write a single response to stdout, and then
exit. Supported methods are:

- `paxm.health`
- `paxm.search`
- `paxm.put`
- `paxm.putBatch` (optional; paxm falls back to repeated `paxm.put` when the
  plugin returns JSON-RPC `-32601 Method not found`)

`paxm.search` receives a `SearchQuery` JSON object and returns
`{"hits":[...]}`. `paxm.put` receives a `MemoryItem` JSON object and returns
`{"ref":{...}}` or `{"refs":[...]}`. `paxm.putBatch` receives
`{"items":[...]}` and returns `{"refs":[...]}`. `SearchQuery` may include
`tiers`; `MemoryItem` may include `tier` and `expires_at`.

Legacy configs with a default `local` provider are loaded as a `sqlite`
provider; a legacy `*.jsonl` path is mapped to the same basename with a
`*.sqlite` extension. JSONL memory contents are not migrated.

## Recall Profiles

`recall_profiles` defines read strategy.

The default config separates explicit and passive recall:

- `default` is used by active `paxm recall`, reads both `stm` and `ltm`, and
  returns 3 memories by default. Use `paxm recall --limit N` to request more for
  a specific query.
- `passive_initial` is used only for the first `user_input` observed in a
  session. It reads `ltm` only, is looser, and returns up to 5 memories for
  session warmup context.
- `passive` is used by later hook-based `user_input` calls and is intentionally
  narrower, with higher thresholds, fewer results, and `ltm`-only reads.

Provider route fields:

- `name`: provider instance name from `providers`.
- `required`: when true, a provider error fails the recall command.
- `weight`: multiplier applied after provider relevance normalization.
- `thresholds`: optional provider-specific recall thresholds. When present,
  non-zero `min_relevance` or `min_score` values override the profile-level
  defaults for this provider route only.
- `tiers`: optional memory tiers to search. Supported values are `stm` and
  `ltm`; omitted means no tier filter. Unknown values are configuration errors.

Threshold fields:

- `min_relevance`: adapter-normalized relevance threshold before merge.
- `min_score`: provider-normalized score threshold after route weight and
  recency. Its existing meaning is unchanged by cross-provider calibration.

Recall timeout fields:

- A provider route `timeout` limits that downstream independently. Passive
  profiles default to `250ms`, so one slow provider cannot delay healthy hits.
- A hook recall `timeout_extra` derives the complete passive recall deadline as
  the largest selected provider-route timeout plus this scheduling margin. New
  configurations default the margin to `100ms`, so every provider gets its own
  configured budget while the hook remains bounded. The legacy absolute
  `timeout` remains supported when `timeout_extra` is omitted.

Write provider routes use the same `timeout` field. Write profiles default to
`30s`; a timed-out optional provider is reported without delaying healthy
providers, while a timed-out required provider makes the write fail. A
single-call bulkhead prevents repeated writes from piling up behind a provider
that ignores cancellation.

Example provider-specific passive recall thresholds:

```yaml
recall_profiles:
  passive:
    providers:
      - name: sqlite
        required: true
        weight: 1
        timeout: 250ms
      - name: mem0_cloud
        required: false
        weight: 1
        timeout: 800ms
        thresholds:
          min_relevance: 0.20
          min_score: 0.20
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
```

Ranking fields:

- `type`: currently `weighted_relevance`. Before applying route weights, paxm
  derives an internal provider-independent ranking signal as adapter-normalized
  relevance divided by the square of provider-local rank. This limits flat-score providers
  without promoting weak evidence. It is deterministic normalization, not
  probability calibration.
- `recency_boost`: optional bounded recency signal added after calibrated
  relevance is multiplied by the route weight. The final score can exceed `1`
  when a route weight exceeds `1`, preserving existing multiplier semantics.

Recall JSON exposes three score layers: `raw_score` is the untouched backend
value, `relevance` is the adapter-normalized `[0,1]` value, and `score` applies
route weight and recency to that value. Cross-provider calibration remains an
internal router concern so public provider, facade, CLI, MCP, and JSON-RPC
interfaces do not gain calibration complexity. The same algorithm covers
SQLite, Zep, Mem0, Mem0 Cloud, and JSON-RPC without vendor-specific multipliers.

## Write Profiles

`write_profiles` defines write strategy. A write profile chooses provider routes,
the memory tier assigned to each write, and an optional expiry. `ltm` is durable
long-term memory. `stm` is short-term working memory and defaults to
`expires_after: 24h`.

Tier and expiry configuration is strict: every `stm` write profile must set a
positive Go duration in `expires_after`, and an `ltm` profile must not set
`expires_after`. Invalid configuration fails before an existing config file is
overwritten.

`paxm remember` uses the `default` write profile unless another profile is
selected. Agent skills should use `--profile stm` for short-lived task state and
`--profile ltm` for durable preferences, decisions, or recurring fixes.

For LTM writes without an explicit ID, paxm derives a stable ID from normalized
text and optional `workspace` metadata. SQLite consolidates exact repeats and
stores lifecycle metadata under `paxm_fingerprint`, `paxm_occurrences`,
`paxm_first_seen_at`, and `paxm_last_seen_at`. Different workspaces remain
separate. STM and explicit-ID writes are not content-addressed. Near-duplicate or
contradictory wording is not merged automatically. Passive `user_input` writes
use the prompt as their stable identity basis while retaining the full rendered
hook template as stored evidence.

Buffered hook writes use the completed episode as an unbounded turn boundary.
SQLite adds `session_id`, `turn_id`, `started_at`, and `ended_at` to that turn's
stored metadata. Historical SQLite backfill also keeps one item per normalized
turn; remote providers keep the existing bounded multipart behavior. These are
internal behaviors and have no user-facing tuning knobs.

## Agents

`agents.<name>.active_recall` controls explicit recall calls made by that agent
or by a skill running inside that agent. Setup preserves this field for
compatibility but does not install or configure active recall skills.

`agents.codex.hooks.user_input.recall` controls passive recall from the Codex
`UserPromptSubmit` hook.

`agents.claude.hooks.user_input.recall` controls passive recall from Claude
Code's `UserPromptSubmit` hook. Setup registers Claude Code hooks in
`~/.claude/settings.json`, or under `CLAUDE_CONFIG_DIR` when it is set. The
settings merge preserves existing hooks and writes a one-time `.paxm.bak`
backup before changing an existing file.

`agents.pi.hooks.user_input.recall` controls passive recall from Pi's
`before_agent_start` extension event.

`agents.pi.hooks.turn_end.write` controls best-effort passive writes from Pi.
The generated Pi extension buffers the current prompt and `message_end` events
in memory, correlates `tool_execution_start` args with `tool_execution_end`
results, then sends the complete run to paxm on Pi's `agent_end` runtime event.
It also tries one final flush on `session_shutdown`. Pi's lifecycle events use
the runtime event bus, so treat them as best-effort rather than a hard delivery
guarantee.

`agents.opencode.hooks.user_input.recall` controls passive recall from
OpenCode's `chat.message` plugin hook. The generated plugin runs the bounded
paxm recall shim, then injects admitted hits only into the current model-message
transform. It does not modify the user message stored by OpenCode.

`agents.opencode.hooks.turn_end.write` controls durable passive writes from
OpenCode's `session.idle` event. The plugin reads the completed turn through the
official OpenCode client, keeps visible user and assistant text, and excludes
reasoning and tool parts before sending the episode to paxm. Repeated idle
events for the same final assistant message are deduplicated in the plugin.

`agents.<name>.integration.owner` records which installation surface owns the
agent lifecycle hooks. An empty value preserves the original paxm-managed
behavior. For Codex plugin installations, run:

```bash
paxm setup --integration codex-plugin
```

This records `owner: codex-plugin`, removes legacy paxm-managed Codex hook
registrations, and lets the bundled `paxm-memory` plugin provide the hooks. The
plugin marks its calls with `PAXM_INTEGRATION_OWNER=codex-plugin`; paxm ignores
plugin calls until that owner is configured and ignores legacy Codex calls after
ownership is transferred. This prevents duplicate recall and write events when
both installation surfaces are present.

Hook recall fields:

- `profile`: recall profile used to fetch candidates. The default hook uses
  `passive`.
- `query_template`: Go template rendered from hook data into the recall query.
- `max_results`: per-hook result limit passed to recall.
- `insertion.min_score`: second-pass score threshold before hook output is
  returned to the agent context.
- `insertion.max_items`: maximum number of memories inserted by the hook.
- `insertion.require_query_terms`: when true, a hit must contain at least one
  significant query term before it is inserted.
- `initial`: optional first-user-input override. When enabled, paxm tracks
  recent session keys locally and applies this looser recall profile only once
  per target/session before falling back to the normal strict hook config.

`agents.<name>.hooks.*.write` controls passive hook writes. Codex and Claude
Code share the main lifecycle events:

- `session_start`: native `SessionStart`; writes a session-start event into the
  buffer.
- `user_input`: native `UserPromptSubmit`; returns recall output and writes the
  user input event into the buffer.
- `tool_use` (Claude Code): native `PostToolUse`; writes normalized tool name,
  input, and result while recursively excluding thinking/reasoning fields.
- `tool_failure` (Claude Code): native `PostToolUseFailure`; writes the tool
  name, input, and error for failed or interrupted calls.
- `turn_end`: native `Stop`; writes a turn-end event and flushes the buffer.

Codex `turn_end` also reads the current local transcript and extracts tool
calls/results before flushing, which covers file-reading tools that do not emit
Codex `PostToolUse`. Transcript parsing is best-effort and fail-open.

Claude Code supplies `last_assistant_message` in the raw `Stop` payload, so the
default `turn_end` template sends the final assistant response to the configured
write providers without storing the rest of the raw runtime event. Claude Code
receives admitted recall hits as Markdown context from the synchronous
`UserPromptSubmit` hook.

For Pi, the paxm `turn_end` hook maps to Pi's runtime `agent_end` event and
receives the complete buffered run from the generated extension. For OpenCode,
it maps to `session.idle` and receives the last completed user/assistant turn.

Hook write fields:

- `enabled`: whether this hook produces a memory write item.
- `profile`: write profile used when the item is flushed. Built-in passive
  hooks default to `ltm`.
- `template`: Go template rendered from hook data. Built-in defaults use
  `.safe_text`, a filtered view of user input, visible assistant output,
  normalized tool calls/results, Pi turn messages, and workspace context.
  Thinking, reasoning, analysis, and unrelated runtime payloads are excluded.
  Available keys also include `.target`,
  `.event`, `.prompt`, `.assistant`, `.messages`, `.query`, `.workspace`,
  `.metadata`, and `.raw_json`. Use `.raw_json` only for explicit custom debug
  capture; it is not part of the default long-term memory templates.
- `mode`: descriptive write mode for config readability.
- `buffer.enabled`: when true, append this hook item to the durable capture
  queue.
- `buffer.flush`: when true, seal this session's pending events as a complete
  episode after appending this item.
- `buffer.flush_count`: retained for configuration compatibility. The durable
  queue no longer needs a global item-count flush; `turn_end` and
  `capture_queue.max_episode_age` define episode boundaries.

Paxm writes the rendered template output as `MemoryItem.text`. The default
template stores semantic hook content instead of raw runtime events. Providers
own any additional extraction behavior: SQLite stores the text directly, Zep
receives it as a text episode, Mem0 receives it as a `role=user` message and may
infer memories server-side, and JSON-RPC plugins decide their own extraction or
storage behavior.

For providers without native tier or TTL fields, paxm stores `paxm_tier` and
`paxm_expires_at` metadata and filters results in the core router.

The hook queue is a SQLite WAL-backed log owned by a short-lived local daemon.
It is durable across daemon restarts, partitions events by agent session, and
acknowledges hooks after the local transaction commits. By default it is stored
at `hooks/capture.sqlite` beside the paxm config.

Top-level `capture_queue` settings control delivery behavior:

```yaml
capture_queue:
  # Optional; defaults to <config-dir>/hooks/capture.sqlite.
  path: ~/.config/paxm/hooks/capture.sqlite
  # Seal a session without turn_end as an incomplete episode.
  max_episode_age: 1m
  # Initial retry delay; later failures use bounded exponential backoff.
  retry_min: 1s
  # Move a repeatedly failing delivery to dead-letter state.
  max_attempts: 10
  provider_concurrency:
    sqlite: 1
    default: 4
```

Different providers are delivered concurrently and maintain independent ACK and
retry state. Episodes remain ordered within the same provider/session pair, so a
later episode cannot bypass an earlier failed delivery. `hook_delivery`
telemetry records the episode, session, provider, reference, error, provider call
duration, and average per-message time from durable capture to provider ACK.
`paxm history` reports the last two values as `avg_write` and
`avg_passive_latency` for each provider. Only successful ACKs contribute to the
averages.

After any successful daemon flush or immediate hook write, paxm schedules
expired-memory cleanup on a single daemon worker. This is best effort and only
affects providers that implement cleanup. Hook responses do not wait for cleanup,
but daemon shutdown drains work that was already scheduled. SQLite deletes a
bounded batch of expired rows; recalls already filter expired items even before
storage cleanup runs.

## Setup And Uninstall

In a TTY, multi-select prompts use up/down, space, and enter. After selecting
agents, setup configures each one in the fixed order Codex, Claude Code, Pi, and
OpenCode.
Per-agent setup controls only passive recall, passive write, profiles, and write
events. Non-TTY input retains the numbered selector for scripts and tests.

`paxm uninstall` removes every built-in passive integration. Pass
`--agent codex`, `--agent claude`, `--agent pi`, or `--agent opencode` to remove
one. The command
preserves hook details in paxm config while setting the selected agent's
`enabled` field to false, so a later setup can reuse the previous choices.
Provider config, memory data, telemetry, active skills, the binary, and `.paxm.bak`
files are not removed.

When setup first enables an agent, it records:

```yaml
agents:
  codex:
    enabled: true
    passive_write_started_at: "2026-07-09T18:30:00Z"
```

`passive_write_started_at` is the default exclusive cutoff for `paxm backfill`. Setup does
not replace an existing value when an integration is reconfigured. Configs
created before this field was introduced must pass `--before` to `backfill
scan` and `backfill run`.

Backfill targets one exact enabled provider instance with `--provider`; it does
not fan out through a write profile. Foreground mode displays progress, speed,
and ETA. `--background` starts a silent detached worker. Both modes share a
per-agent/provider process lock and persistent success ledger, so concurrent or
repeated commands do not upload already completed items again. Runtime status,
the lock, the background log, and `backfill.sqlite` are stored below
`<telemetry.dir>/backfill/`.

## Telemetry

`telemetry` controls local debug logs and metrics used by `paxm history`.

Files:

- `events_file`: rolling JSONL event log. It records operation metadata such as
  command, hook event, profile, hit count, provider names, duration, and errors.
- `metrics_file`: compact aggregate JSON. It is overwritten on each telemetry
  update and is the source for `paxm history`.
- Agent metrics aggregate passive hook recall and write counts by hook target,
  such as `codex`.
- Provider metrics aggregate recall calls, write calls, hits, refs, and provider
  errors by provider name. Recall telemetry also records each provider's
  duration, configured timeout, outcome, and bulkhead state. Aggregates expose
  average and histogram-based p95 recall latency, timeout count, and bulkhead
  skip count.
- Passive hook events set `recall_timed_out` when the overall recall budget is
  reached, independently of per-provider timeout outcomes.

Bounds:

- `max_event_file_bytes`: rotate `events_file` when the next event would exceed
  this size.
- `max_event_files`: total event log files to keep, including the active file.
  With the default `3`, paxm keeps `events.jsonl`, `events.jsonl.1`, and
  `events.jsonl.2`.
- `retention_days`: number of daily metric buckets to keep in `metrics_file`.

Read telemetry through:

```bash
paxm history --days 7          # aggregate metrics
paxm logs --tail 100           # recent raw events across rotated files
paxm logs --tail 0 --follow    # new events only, following rotation
paxm logs --tail 100 --json    # raw JSONL output
```

`paxm logs --follow` stops on Ctrl-C. Raw logs remain a local CLI surface and
are not exposed by `paxm mcp serve`.

Privacy:

- Query text is not stored by default. Telemetry stores query length and a
  SHA-256 hash prefix for correlation.
- Set `capture_query_preview: true` only if local debugging needs a short query
  preview. The preview is capped by `query_preview_chars`.
