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
  local:
    type: local
    enabled: true
    path: ~/.local/share/paxm/memory.jsonl

  mem0:
    type: mem0
    enabled: false
    api_key: "plain-text-api-key"

  zep:
    type: zep
    enabled: false
    api_key: "plain-text-zep-api-key"
    user_id: todd
    search_scope: episodes

recall_profiles:
  default:
    providers:
      - name: local
        required: true
        weight: 1.0
    max_results: 8
    thresholds:
      min_relevance: 0.25
      min_score: 0.25
    ranking:
      type: weighted_relevance
      recency_boost: 0

  passive:
    providers:
      - name: local
        required: true
        weight: 1.0
    max_results: 2
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
    ranking:
      type: weighted_relevance
      recency_boost: 0

write_profiles:
  default:
    providers:
      - name: local
        required: true

agents:
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
```

## Providers

`providers` declares provider instances and connection details.

Fields:

- `type`: adapter type, such as `local`.
- `enabled`: whether this provider can be used by profiles.
- `path`: local provider JSONL path.
- `api_key`: optional plain-text API key for remote providers.
- `base_url`: optional remote provider API base URL override.
- `user_id`: Zep user graph target.
- `graph_id`: Zep named graph target.
- `search_scope`: Zep graph search scope. Supported values are `episodes`,
  `edges`, `nodes`, `observations`, `thread_summaries`, and `auto`.
- `max_characters`: optional Zep auto-scope context character limit.
- `source_description`: optional Zep source description for writes.

V1 ships with `local` and `zep` provider adapters. Zep requires `api_key` and
exactly one of `user_id` or `graph_id`. If setup is configured for a Zep user
graph, it idempotently creates the configured `user_id` when the user does not
already exist.

## Recall Profiles

`recall_profiles` defines read strategy.

The default config separates explicit and passive recall:

- `default` is used by active `paxm recall` and can be broader.
- `passive` is used by Codex `UserPromptSubmit` and is intentionally narrower,
  with higher thresholds and fewer results.

Provider route fields:

- `name`: provider instance name from `providers`.
- `required`: when true, a provider error fails the recall command.
- `weight`: multiplier applied after provider relevance normalization.

Threshold fields:

- `min_relevance`: provider-normalized relevance threshold before merge.
- `min_score`: final score threshold after weight and ranking boosts.

Ranking fields:

- `type`: currently `weighted_relevance`.
- `recency_boost`: optional boost added from item age when `created_at` exists.

## Write Profiles

`write_profiles` defines write strategy. `paxm remember` uses the `default`
write profile unless another profile is selected.

## Agents

`agents.codex.active_recall` controls explicit recall calls.

`agents.codex.hooks.user_input.recall` controls passive recall from the Codex
`UserPromptSubmit` hook.

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

`agents.codex.hooks.*.write` controls passive hook writes. The built-in Codex
event names are:

- `session_start`: Codex `SessionStart`; writes a session-start event into the
  buffer.
- `user_input`: Codex `UserPromptSubmit`; returns recall output and writes the
  user input event into the buffer.
- `turn_end`: Codex `Stop`; writes a turn-end event and flushes the buffer.

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

The hook buffer is process memory owned by a short-lived local daemon. It is not
durable; if the daemon exits before a flush, buffered hook write items can be
lost.
