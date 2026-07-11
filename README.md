# paxm

`paxm` is a Go CLI for giving agents a stable memory recall surface while leaving setup, API keys, hooks, and provider policy under user control.

## Install

Install the latest release for your local OS and architecture:

```bash
curl -fsSL https://github.com/pax-beehive/memory-adaptor/releases/latest/download/install.sh | bash
```

The installer prints the PAX banner, detects `darwin` or `linux` plus
`amd64` or `arm64`, downloads the matching release archive, verifies it against
`SHA256SUMS`, and installs `paxm`.

Useful overrides:

```bash
# Install a specific version.
curl -fsSL https://github.com/pax-beehive/memory-adaptor/releases/latest/download/install.sh | PAXM_VERSION=v0.1.2 bash

# Install somewhere other than the default writable bin directory.
curl -fsSL https://github.com/pax-beehive/memory-adaptor/releases/latest/download/install.sh | PAXM_INSTALL_DIR="$HOME/go/bin" bash
```

## Agent Skill

This repo includes a bundled agent skill at [skills/paxm/SKILL.md](skills/paxm/SKILL.md).
The skill teaches an agent to actively call `paxm recall`, `paxm remember`, and
`paxm history` while respecting user-owned setup and provider configuration.
Agents that support MCP can instead connect to `paxm mcp serve` for the same
recall, remember, history, and config doctor operations through structured tool
calls.

Install the CLI first, then run one interactive setup pass:

```bash
paxm setup
paxm config doctor
```

`paxm setup` is where the user chooses memory providers and passive agent
integrations. In a terminal, use up/down to move, space to toggle, and enter to
confirm. Selected agents are configured one at a time for passive recall and
passive writes. Active recall skills are installed separately by the user. The
SQLite provider works without an API key; remote providers such as Zep or Mem0
require the user to provide their connection details during setup.

When Codex is using the bundled `paxm-memory` plugin, let the plugin own Codex's
hooks so paxm does not register a duplicate global hook:

```bash
paxm setup --integration codex-plugin
```

To install the skill for Codex or Claude Code, ask an agent to read this
repository, inspect `skills/paxm/SKILL.md`, and install the `paxm` skill into the
active agent skill directory. Direct local installs look like:

```bash
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R skills/paxm "${CODEX_HOME:-$HOME/.codex}/skills/paxm"

mkdir -p "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills"
cp -R skills/paxm "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills/paxm"
```

## V1 Shape

```text
Human setup:
  paxm setup  # choose providers and passive agent integrations interactively

Agent active recall:
  paxm recall --query "what did we decide?" --limit 10 --json

MCP active recall:
  paxm mcp serve  # stdio MCP server for structured agent tool calls

Hook passive recall:
  installed hook shim -> paxm __hook -> in-memory buffer daemon

Local history:
  paxm history --days 7

Local telemetry logs:
  paxm logs --tail 50
  paxm logs --follow

Historical session backfill:
  paxm backfill scan --agent codex --before 2026-07-09
  paxm backfill run --agent codex --provider mem0-company --background
```

The CLI command layer does not talk to concrete memory providers directly. Commands call the facade, the facade calls the memory router, and the router fans out to enabled providers.

```text
cmd/paxm
  internal/cli
  internal/mcp           stdio MCP server and memory tools
  internal/runtime       shared config, router, and facade loading
  internal/facade
  internal/memory        provider interface and multi-provider router
  internal/adapters      provider registry
  internal/adapters/jsonrpc
  internal/adapters/mem0
  internal/adapters/sqlite
  internal/adapters/zep
  internal/config
  internal/telemetry   bounded event logs and metrics
```

## Quick Start

Manual install from a GitHub release:

```bash
VERSION=v0.1.2
curl -L "https://github.com/pax-beehive/memory-adaptor/releases/download/${VERSION}/paxm_${VERSION}_darwin_arm64.tar.gz" -o /tmp/paxm.tar.gz
tar -xzf /tmp/paxm.tar.gz -C /tmp
install /tmp/paxm_${VERSION}_darwin_arm64/paxm ~/go/bin/paxm
paxm version
```

Future upgrades can be installed in place:

```bash
paxm update --check
paxm update
```

Build locally:

```bash
go build -o /tmp/paxm ./cmd/paxm
/tmp/paxm setup
/tmp/paxm remember --profile stm --text "paxm supports hook passive recall"
/tmp/paxm recall --query "passive recall"
/tmp/paxm history --days 7
/tmp/paxm logs --tail 20
```

For a project-local config during development:

```bash
/tmp/paxm --config /tmp/paxm-dev/config.yaml setup --force
/tmp/paxm --config /tmp/paxm-dev/config.yaml remember --profile stm --text "enabled providers can read and write"
printf '{"prompt":"enabled providers"}' | /tmp/paxm --config /tmp/paxm-dev/config.yaml recall --hook-event --json
```

## MCP Server

`paxm` can run as a local stdio MCP server:

```bash
paxm mcp serve
```

MCP hosts should configure the command as `paxm` with args
`["mcp", "serve"]`. If you use a non-default config path, pass it before the
subcommand:

```json
{
  "command": "paxm",
  "args": ["--config", "/path/to/config.yaml", "mcp", "serve"]
}
```

The MCP server exposes a narrow tool surface:

- `paxm_recall`: active recall through configured recall profiles.
- `paxm_remember`: writes through configured write profiles; use `stm` for
  short-term working memory and `ltm` for durable facts.
- `paxm_history`: recent local telemetry summary.
- `paxm_config_doctor`: provider health checks without returning config secrets.

`paxm mcp serve` does not run setup, install hooks, uninstall integrations, or
run historical backfills. Users still own provider credentials, setup, and
passive hook installation through `paxm setup`.

## Config

Default config path:

```text
~/.config/paxm/config.yaml
```

V1 ships with a SQLite provider so the full flow works without external API keys.
The CLI can load legacy JSON configs, but new setup writes YAML by default.

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
        weight: 1
    max_results: 3
    thresholds:
      min_relevance: 0.25
      min_score: 0.25
    ranking:
      type: weighted_relevance
    tiers: [stm, ltm]

  passive:
    providers:
      - name: sqlite
        required: true
        weight: 1
    max_results: 2
    thresholds:
      min_relevance: 0.75
      min_score: 0.75
    ranking:
      type: weighted_relevance
    tiers: [ltm]

  passive_initial:
    providers:
      - name: sqlite
        required: true
        weight: 1
    max_results: 5
    thresholds:
      min_relevance: 0.35
      min_score: 0.35
    ranking:
      type: weighted_relevance
    tiers: [ltm]

write_profiles:
  default:
    tier: ltm
    providers:
      - name: sqlite
        required: true
  stm:
    tier: stm
    expires_after: 24h
    providers:
      - name: sqlite
        required: true
  ltm:
    tier: ltm
    providers:
      - name: sqlite
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

The generated config also includes an opt-in `agents.claude` entry with the
same `session_start`, `user_input`, and `turn_end` lifecycle as Codex. Run
`paxm setup` and select `claude` to install it.

Multiple enabled provider instances are supported by configuration. The key
under `providers` is an instance name, not the adapter type, so configs can have
multiple `mem0` or `jsonrpc` instances such as `mem0_personal`, `mem0_team`, and
`corp_memory`. Recall profiles decide which provider instances are read, how
provider relevance is weighted, what thresholds are applied, and which memory
tiers are searched. The default active recall profile reads both short-term
memory (`stm`) and long-term memory (`ltm`) and returns 3 memories; pass
`--limit N` to `paxm recall` to request more for a specific query. Passive
recall profiles read `ltm` only. Write profiles decide which provider instances
are written and whether the item is stored as `stm` or `ltm`; the default `stm`
profile expires after 24 hours. Configuration rejects unknown tier names. Every
`stm` write profile must set a positive `expires_after`, while `ltm` profiles
must not set an expiry.
Optional provider failures are returned as provider errors; required provider
failures fail the command.

The router requests a bounded candidate pool from each provider before applying
thresholds, cross-provider deduplication, and the final result limit. Duplicate
text keeps the highest-scoring hit, with deterministic tie-breaking, so provider
response timing does not change recall output.

Hook recall uses two passive profiles by default. The first `user_input` seen
for a session can use the looser `passive_initial` profile as session warmup
context. Later `user_input` hooks use the stricter `passive` profile to avoid
polluting the agent context.

Passive hook writes use the `ltm` write profile by default. Active agents should
write short-lived task state to `stm` and reserve `ltm` for durable preferences,
decisions, and recurring fixes.

LTM writes without an explicit ID pass through deterministic admission before
provider fan-out. Paxm normalizes text case and whitespace, scopes it by the
`workspace` metadata value when present, and assigns a stable content-derived ID
plus `paxm_fingerprint`, `paxm_occurrences`, `paxm_first_seen_at`, and
`paxm_last_seen_at` metadata. SQLite uses that identity to consolidate exact
repeats while preserving the first creation time and updating occurrence/seen
metadata. STM writes and explicit IDs, including backfill IDs, are unchanged.
This is exact consolidation, not semantic or LLM-based conflict resolution;
remote providers receive the stable identity metadata but retain their own
deduplication behavior. For passive `user_input` writes, the stable prompt is
used as the identity basis while the stored text still retains the configured
full hook evidence, so volatile session fields do not defeat consolidation.

Expired memory cleanup is hook-triggered and best effort. After a successful
hook-buffer flush or immediate hook write, the hook daemon schedules cleanup on
a single background worker for providers that support it; SQLite deletes a
bounded batch of expired rows. Hook responses do not wait for cleanup, but daemon
shutdown drains already scheduled cleanup before exiting. Recall still filters
expired items even if cleanup has not run yet.

Remote provider configs may include a plain-text `api_key` field. Zep is
supported with `type: zep` using `github.com/getzep/zep-go/v3`; configure
exactly one of `user_id` or `graph_id`. When setup is configured for a Zep
user graph, it ensures the configured `user_id` exists before saving the config.
Self-hosted Mem0 is supported with `type: mem0`; configure `base_url` without a
`/v1` prefix and scope it with at least one of `user_id`, `agent_id`, or `run_id`.
The Mem0 adapter sends `api_key` as `X-API-Key`, matching the OSS REST server.
Custom plugin providers are supported with `type: jsonrpc`. V1 supports stdio
plugins: paxm invokes the configured command with a JSON-RPC 2.0 request for
`paxm.health`, `paxm.search`, `paxm.put`, or optional `paxm.putBatch`.
`SearchQuery` may include `tiers`; `MemoryItem` may include `tier` and
`expires_at`.

`paxm setup` is the interactive entry point for changing provider and passive
integration choices. TTY sessions use checkbox selectors; piped input retains
the numbered text fallback. After the agent selector, each selected agent is
configured in stable order for passive recall profile, passive write profile,
and write events. Setup shows a summary before saving, installs only enabled
hook events, and does not install active recall skills. Codex hooks are
registered in the user-level Codex config, Claude Code hooks in the user-level
Claude settings, and Pi support as a Pi extension.

`paxm history` reads local telemetry metrics and summarizes recall frequency,
hits, hook insertions, writes, provider errors, and storage usage. It breaks
down passive hook recall/write counts by agent, and provider recall/write counts
by provider. Telemetry uses a bounded rolling JSONL event log plus a compact
metrics JSON file. By default it records query length and a query hash, not raw
query text.

`paxm logs` reads the raw rolling event log across retained backups and the
active file. It prints the most recent 50 events in a compact human-readable
format by default. Use `--tail N` to change the initial window, `--json` for
JSONL, and `--follow` to stream new events across active-file rotation until
Ctrl-C. `--tail 0 --follow` starts with new events only. This local debugging
surface is intentionally not exposed through MCP.

`paxm backfill` imports local sessions created before passive integration into
one exact provider instance. Built-in readers support Codex, Claude Code, and
Pi. The default foreground mode prints progress, upload speed, and ETA. Add
`--background` to start a silent detached worker, then inspect it with
`paxm backfill status --agent AGENT --provider NAME`.

```bash
paxm backfill scan --agent codex --before 2026-07-09
paxm backfill run --agent codex --provider mem0-company --rate 30/m
paxm backfill run --agent claude --provider archive --rate 10/m --background
paxm backfill status --agent claude --provider archive
```

Setup records `agents.<name>.passive_write_started_at` the first time passive
write is enabled for an agent. Backfill uses that as its default exclusive cutoff. Configs created
before this field existed must pass `--before` explicitly. Only user and
assistant text is imported; system prompts, hidden reasoning, tool calls, tool
results, sidechains, and attachments are excluded. Long turns are split into
bounded items while preserving source timestamps and session metadata.

Backfill state lives under the configured telemetry state directory. A process
lock permits only one active worker for each config, agent, and provider tuple.
A persistent SQLite ledger skips successfully uploaded item IDs on every later
run, so repeatedly starting the same backfill resumes instead of uploading the
same turns again. A crash after a remote provider accepts an item but before the
local ledger commits remains a narrow duplicate window for providers that do
not enforce the deterministic `paxm_id` themselves.

For Codex, setup writes a shim under the paxm config directory:

```text
~/.config/paxm/hooks/codex-session_start
~/.config/paxm/hooks/codex-user_input
~/.config/paxm/hooks/codex-turn_end
```

It also updates:

```text
~/.codex/config.toml
```

The shims expect hook event JSON on stdin and call a hidden `paxm __hook`
entrypoint. `user_input` returns recall JSON to Codex and also appends a write
item to the in-memory hook buffer. `session_start` appends a write item.
`turn_end` appends a write item and flushes the buffer to the configured write
profile. The buffer lives in a short-lived local daemon and is intentionally not
durable. Codex may still require you to review and trust the new non-managed
hooks with `/hooks` before they run.

For Claude Code, setup writes three shims and updates the user-level settings:

```text
~/.config/paxm/hooks/claude-session_start
~/.config/paxm/hooks/claude-user_input
~/.config/paxm/hooks/claude-turn_end
~/.claude/settings.json
```

The settings update preserves existing hooks, avoids duplicate paxm entries,
and creates `~/.claude/settings.json.paxm.bak` before the first modification.
Claude Code `SessionStart`, `UserPromptSubmit`, and `Stop` map to paxm
`session_start`, `user_input`, and `turn_end`. Recall is returned as plain
Markdown from `UserPromptSubmit`, which Claude Code adds to the prompt context.
The `Stop` payload includes Claude Code's `last_assistant_message`; paxm writes
that visible assistant text and flushes the buffered session/user/turn evidence
without storing the rest of the raw runtime event by default.

For Pi, setup writes paxm hook shims and registers a Pi extension:

```text
~/.config/paxm/hooks/pi-user_input
~/.config/paxm/hooks/pi-turn_end
~/.pi/agent/extensions/paxm-hook/index.ts
```

The Pi extension listens for Pi's `before_agent_start` extension event and calls
the paxm `user_input` hook shim. It also buffers the current prompt and Pi
`message_end` events in memory, then calls `pi-turn_end` on Pi's runtime
`turn_end` event. A `session_shutdown` handler makes one final best-effort
flush. Because Pi `message_end` and `turn_end` are runtime event-bus events
rather than the typed `before_agent_start` surface, Pi passive writes are
best-effort and should be verified with `paxm history`.

## Uninstall Passive Integrations

Remove all paxm-managed passive agent integrations:

```bash
paxm uninstall
```

Remove only one integration, or skip confirmation for automation:

```bash
paxm uninstall --agent claude
paxm uninstall --agent codex --yes
```

Uninstall disables the selected agent in paxm config, removes only paxm-owned
hook entries and shims, and best-effort flushes the hook buffer first. It does
not delete provider configuration, SQLite or remote memory data, telemetry,
the paxm binary, settings backups, or active recall skills installed by the
user. Repeating the command is safe.

## Releases

Release binaries are built by GitHub Actions when a `v*` tag is pushed. The
release workflow runs `go test ./...`, builds `paxm` for darwin, linux, and
windows on amd64 and arm64, uploads archives, and publishes `SHA256SUMS`.
Released binaries support `paxm update`, which downloads the current platform's
archive from GitHub releases, verifies it against `SHA256SUMS`, and replaces the
current executable. Use `paxm update --version vX.Y.Z` to pin a specific release
or `paxm update --install-path PATH` to install somewhere else.

To build the same assets locally:

```bash
VERSION=v0.1.0 scripts/build-release.sh
ls dist/
```

See [docs/release.md](docs/release.md) for the release checklist.
