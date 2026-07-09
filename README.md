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

Install the CLI first, then run one interactive setup pass:

```bash
paxm setup
paxm config doctor
```

`paxm setup` is where the user chooses memory providers and agent hooks. Active
recall requires at least one enabled readable provider. The SQLite provider works
without an API key; remote providers such as Zep require the user to provide
their API key during setup.

To install the skill for Codex, ask an agent to read this repository, inspect
`skills/paxm/SKILL.md`, and install the `paxm` skill into the active Codex skill
directory. A direct local install looks like:

```bash
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R skills/paxm "${CODEX_HOME:-$HOME/.codex}/skills/paxm"
```

## V1 Shape

```text
Human setup:
  paxm setup  # choose providers and agent hooks interactively

Agent active recall:
  paxm recall --query "what did we decide?" --limit 10 --json

Hook passive recall:
  installed hook shim -> paxm __hook -> in-memory buffer daemon

Local history:
  paxm history --days 7
```

The CLI command layer does not talk to concrete memory providers directly. Commands call the facade, the facade calls the memory router, and the router fans out to enabled providers.

```text
cmd/paxm
  internal/cli
  internal/facade
  internal/memory        provider interface and multi-provider router
  internal/adapters      provider registry
  internal/adapters/sqlite
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
/tmp/paxm remember --text "paxm supports hook passive recall"
/tmp/paxm recall --query "passive recall"
/tmp/paxm history --days 7
```

For a project-local config during development:

```bash
/tmp/paxm --config /tmp/paxm-dev/config.yaml setup --force
/tmp/paxm --config /tmp/paxm-dev/config.yaml remember --text "enabled providers can read and write"
printf '{"prompt":"enabled providers"}' | /tmp/paxm --config /tmp/paxm-dev/config.yaml recall --hook-event --json
```

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

write_profiles:
  default:
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

Multiple enabled providers are supported by configuration. Recall profiles decide
which providers are read, how provider relevance is weighted, and what
thresholds are applied. The default active recall profile returns 3 memories;
pass `--limit N` to `paxm recall` to request more for a specific query. Write
profiles decide which providers are written.
Optional provider failures are returned as provider errors; required provider
failures fail the command.

Hook recall uses two passive profiles by default. The first `user_input` seen
for a session can use the looser `passive_initial` profile as session warmup
context. Later `user_input` hooks use the stricter `passive` profile to avoid
polluting the agent context.

Remote provider configs may include a plain-text `api_key` field. Zep is
supported with `type: zep` using `github.com/getzep/zep-go/v3`; configure
exactly one of `user_id` or `graph_id`. When setup is configured for a Zep
user graph, it ensures the configured `user_id` exists before saving the config.

`paxm setup` is the interactive entry point for changing provider and hook choices. It uses numbered selectors for memory providers and agent hooks, then writes the paxm config, installs selected hook shims, and registers selected agent integrations. Codex hooks are registered in the user-level Codex config. Pi support is installed as a Pi extension.

`paxm history` reads local telemetry metrics and summarizes recall frequency,
hits, hook insertions, writes, provider errors, and storage usage. It breaks
down passive hook recall/write counts by agent, and provider recall/write counts
by provider. Telemetry uses a bounded rolling JSONL event log plus a compact
metrics JSON file. By default it records query length and a query hash, not raw
query text.

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

For Pi, setup writes one paxm hook shim and registers a Pi extension:

```text
~/.config/paxm/hooks/pi-user_input
~/.pi/agent/extensions/paxm-hook/index.ts
```

The Pi extension listens for Pi's `before_agent_start` extension event and calls
the paxm `user_input` hook shim. Pi v1 integration is recall-only: it can insert
high-confidence passive recall results before the agent starts a turn, but it
does not currently capture session start or turn-end writes.

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
