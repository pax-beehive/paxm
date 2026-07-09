# paxm

`paxm` is a Go CLI for giving agents a stable memory recall surface while leaving setup, API keys, hooks, and provider policy under user control.

## V1 Shape

```text
Human setup:
  paxm setup  # choose providers and agent hooks interactively

Agent active recall:
  paxm recall --query "what did we decide?" --json

Hook passive recall:
  installed hook shim -> paxm recall --hook-event --json
```

The CLI command layer does not talk to concrete memory providers directly. Commands call the facade, the facade calls the memory router, and the router fans out to enabled providers.

```text
cmd/paxm
  internal/cli
  internal/facade
  internal/memory        provider interface and multi-provider router
  internal/adapters      provider registry
  internal/adapters/local
  internal/config
```

## Quick Start

```bash
go build -o /tmp/paxm ./cmd/paxm
/tmp/paxm setup
/tmp/paxm remember --text "paxm supports hook passive recall"
/tmp/paxm recall --query "passive recall"
```

For a project-local config during development:

```bash
/tmp/paxm --config /tmp/paxm-dev/config.json setup --force
/tmp/paxm --config /tmp/paxm-dev/config.json remember --text "enabled providers can read and write"
printf '{"prompt":"enabled providers"}' | /tmp/paxm --config /tmp/paxm-dev/config.json recall --hook-event --json
```

## Config

Default config path:

```text
~/.config/paxm/config.json
```

V1 ships with a local JSONL provider so the full flow works without external API keys.

```json
{
  "version": 1,
  "providers": {
    "local": {
      "type": "local",
      "enabled": true,
      "read": true,
      "write": true,
      "required": true,
      "path": "~/.local/share/paxm/memory.jsonl",
      "weight": 1
    }
  }
}
```

Multiple enabled providers are supported by configuration. Read-enabled providers are queried concurrently. Write-enabled providers are written concurrently. Optional provider failures are returned as provider errors; required provider failures fail the command.

`paxm setup` is the interactive entry point for changing provider and hook choices. It uses numbered selectors for memory providers and agent hooks, then writes the paxm config, installs selected hook shims, and registers Codex hooks in the user-level Codex config.

For Codex, setup writes a shim under the paxm config directory:

```text
~/.config/paxm/hooks/codex-user_prompt
```

It also updates:

```text
~/.codex/config.toml
```

The shim expects a hook event JSON object on stdin and calls `paxm recall --hook-event --json`. Codex may still require you to review and trust the new non-managed hook with `/hooks` before it runs.
