---
name: paxm
description: Use paxm as Codex's active and passive memory layer. Trigger for prior decisions, project context, user preferences, memory recall, memory writes, or paxm diagnostics.
---

# paxm memory

Use paxm as a supporting memory layer, not as a replacement for current source
verification. Keep setup, credentials, provider selection, and passive hook
policy under the user's control.

## Before using memory

Check the local installation only when the task needs memory:

```bash
paxm version
paxm config doctor
```

If the binary is missing, ask the user before running the bundled installer. If
the config is missing or invalid, ask whether to start the interactive setup
flow. Do not edit paxm YAML by hand.

## Active recall

Use a short, concrete query and a small result limit:

```bash
paxm recall --query "repo decision about hook ownership" --limit 3 --json
```

For a custom config, pass `--config` before the subcommand. Read scores and
provider metadata, then verify facts that may have changed in the current repo.
If a result exposes a precise lead, do at most two or three focused follow-up
queries rather than running an open-ended search loop.

When the host exposes paxm MCP tools, prefer `paxm_recall`, `paxm_remember`,
`paxm_history`, and `paxm_config_doctor` over shelling out.

## Remember

Use STM for task-local working state:

```bash
paxm remember --profile stm --text "Working note: ..."
```

Use LTM only for durable decisions, preferences, recurring fixes, or stable
project conventions:

```bash
paxm remember --profile ltm --text "Decision: ..."
```

Never store secrets, API keys, access tokens, raw private logs, or large pasted
transcripts. Ask before storing sensitive personal or business information.

## Passive recall and diagnostics

Passive Codex hooks are installed by this plugin only after the user completes
the plugin-aware setup flow. Do not run a second `paxm setup` without the
`--integration codex-plugin` mode, because that would change hook ownership.

When debugging, use bounded local history and logs:

```bash
paxm history --days 7 --json
paxm logs --tail 50
```

Treat memory as supporting evidence. Current repository files, current tool
output, and explicit user instructions take precedence over recalled content.
