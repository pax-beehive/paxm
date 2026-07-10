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

  passive:
    providers:
      - name: sqlite
        required: true
        weight: 1.0
    max_results: 2
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
    ranking:
      type: weighted_relevance
      recency_boost: 0

  passive_initial:
    providers:
      - name: sqlite
        required: true
        weight: 1.0
    max_results: 5
    thresholds:
      min_relevance: 0.35
      min_score: 0.35
    ranking:
      type: weighted_relevance
      recency_boost: 0

write_profiles:
  default:
    providers:
      - name: sqlite
        required: true

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
          profile: default
          template: |
            Claude Code session started.

            Event:
            {{ .raw_json }}
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
          profile: default
          template: |
            Claude Code user input:
            {{ .prompt }}

            Event:
            {{ .raw_json }}
          mode: user_input
          buffer:
            enabled: true
            flush_count: 10

      turn_end:
        write:
          enabled: true
          profile: default
          template: |
            Claude Code turn ended.

            Event:
            {{ .raw_json }}
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
          profile: default
          template: |
            Session started.

            Event:
            {{ .raw_json }}
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
          profile: default
          template: |
            User input:
            {{ .prompt }}

            Event:
            {{ .raw_json }}
          mode: user_input
          buffer:
            enabled: true
            flush_count: 10

      turn_end:
        write:
          enabled: true
          profile: default
          template: |
            Turn ended.

            Event:
            {{ .raw_json }}
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
          profile: default
          template: |
            Pi turn ended.

            Event:
            {{ .raw_json }}
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

- `type`: adapter type, such as `sqlite`, `zep`, `mem0`, or `jsonrpc`.
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
- `search_scope`: Zep graph search scope. Supported values are `episodes`,
  `edges`, `nodes`, `observations`, `thread_summaries`, and `auto`.
- `max_characters`: optional Zep auto-scope context character limit.
- `source_description`: optional Zep source description for writes.
- `infer`: optional Mem0 write flag. Omit it to use the server default.

V1 ships with `sqlite`, `zep`, `mem0`, and `jsonrpc` provider adapters. Zep
requires `api_key` and exactly one of `user_id` or `graph_id`. If setup is
configured for a Zep user graph, it idempotently creates the configured
`user_id` when the user does not already exist.

Mem0 is intended for the self-hosted OSS REST server. Configure `base_url`
without a `/v1` prefix, for example `http://localhost:8888`, and set at least
one scope for paxm to use with `user_id`, `agent_id`, or `run_id`. Programmatic
auth uses `api_key` as an `X-API-Key` header; leave it blank only for local Mem0
deployments that intentionally run with auth disabled.

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
`{"items":[...]}` and returns `{"refs":[...]}`.

Legacy configs with a default `local` provider are loaded as a `sqlite`
provider; a legacy `*.jsonl` path is mapped to the same basename with a
`*.sqlite` extension. JSONL memory contents are not migrated.

## Recall Profiles

`recall_profiles` defines read strategy.

The default config separates explicit and passive recall:

- `default` is used by active `paxm recall` and returns 3 memories by default.
  Use `paxm recall --limit N` to request more for a specific query.
- `passive_initial` is used only for the first `user_input` observed in a
  session. It is looser and returns up to 5 memories for session warmup context.
- `passive` is used by later hook-based `user_input` calls and is intentionally
  narrower, with higher thresholds and fewer results.

Provider route fields:

- `name`: provider instance name from `providers`.
- `required`: when true, a provider error fails the recall command.
- `weight`: multiplier applied after provider relevance normalization.
- `thresholds`: optional provider-specific recall thresholds. When present,
  non-zero `min_relevance` or `min_score` values override the profile-level
  defaults for this provider route only.

Threshold fields:

- `min_relevance`: provider-normalized relevance threshold before merge.
- `min_score`: final score threshold after weight and ranking boosts.

Example provider-specific passive recall thresholds:

```yaml
recall_profiles:
  passive:
    providers:
      - name: sqlite
        required: true
        weight: 1
      - name: mem0_team
        required: false
        weight: 1
        thresholds:
          min_relevance: 0.45
          min_score: 0.45
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
```

Ranking fields:

- `type`: currently `weighted_relevance`.
- `recency_boost`: optional boost added from item age when `created_at` exists.

## Write Profiles

`write_profiles` defines write strategy. `paxm remember` uses the `default`
write profile unless another profile is selected.

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
in memory, then sends them to paxm on Pi's `turn_end` runtime event. It also
tries one final flush on `session_shutdown`. Pi's `turn_end` event is used
through the runtime event bus, so treat it as best-effort rather than a hard
delivery guarantee.

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
Code use the same internal event names:

- `session_start`: native `SessionStart`; writes a session-start event into the
  buffer.
- `user_input`: native `UserPromptSubmit`; returns recall output and writes the
  user input event into the buffer.
- `turn_end`: native `Stop`; writes a turn-end event and flushes the buffer.

Claude Code supplies `last_assistant_message` in the raw `Stop` payload, so the
default `turn_end` template sends the final assistant response to the configured
write providers along with the rest of the event. Claude Code receives admitted
recall hits as Markdown context from the synchronous `UserPromptSubmit` hook.

For Pi, `turn_end` maps to Pi's runtime `turn_end` event and receives buffered
Pi messages from the generated extension.

Hook write fields:

- `enabled`: whether this hook produces a memory write item.
- `profile`: write profile used when the item is flushed.
- `template`: Go template rendered from hook data. Available keys include
  `.target`, `.event`, `.prompt`, `.query`, `.workspace`, `.metadata`, and
  `.raw_json`.
- `mode`: descriptive write mode for config readability.
- `buffer.enabled`: when true, queue this hook item in the in-memory daemon.
- `buffer.flush`: when true, flush the current in-memory daemon buffer after
  appending this item.
- `buffer.flush_count`: flush after the buffer reaches this many items.

Paxm writes the rendered template output as `MemoryItem.text`. It does not run a
shared extraction step before writing. Providers own any extraction behavior:
SQLite stores the text directly, Zep receives it as a text episode, Mem0
receives it as a `role=user` message and may infer memories server-side, and
JSON-RPC plugins decide their own extraction or storage behavior.

The hook buffer is process memory owned by a short-lived local daemon. It is not
durable; if the daemon exits before a flush, buffered hook write items can be
lost.

## Setup And Uninstall

In a TTY, multi-select prompts use up/down, space, and enter. After selecting
agents, setup configures each one in the fixed order Codex, Claude Code, and Pi.
Per-agent setup controls only passive recall, passive write, profiles, and write
events. Non-TTY input retains the numbered selector for scripts and tests.

`paxm uninstall` removes every built-in passive integration. Pass
`--agent codex`, `--agent claude`, or `--agent pi` to remove one. The command
preserves hook details in paxm config while setting the selected agent's
`enabled` field to false, so a later setup can reuse the previous choices.
Provider config, memory data, telemetry, active skills, the binary, and `.paxm.bak`
files are not removed.

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
  errors by provider name.

Bounds:

- `max_event_file_bytes`: rotate `events_file` when the next event would exceed
  this size.
- `max_event_files`: total event log files to keep, including the active file.
  With the default `3`, paxm keeps `events.jsonl`, `events.jsonl.1`, and
  `events.jsonl.2`.
- `retention_days`: number of daily metric buckets to keep in `metrics_file`.

Privacy:

- Query text is not stored by default. Telemetry stores query length and a
  SHA-256 hash prefix for correlation.
- Set `capture_query_preview: true` only if local debugging needs a short query
  preview. The preview is capped by `query_preview_chars`.
