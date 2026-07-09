---
name: paxm
description: Use paxm as an active agent memory layer. Trigger when the user asks to recall prior context, search or inspect memory, remember a durable fact, debug paxm history/metrics, or when a task would benefit from active memory recall before answering, especially repo, project, preference, architecture, or previous-decision questions.
---

# paxm

Use `paxm` to actively recall and record memory while keeping setup and provider policy under the user's control.

## Prerequisites

Prefer the installed `paxm` on `PATH`.

Check availability when needed:

```bash
paxm version
paxm config doctor
```

If `paxm` is missing, tell the user to install it from this repository:

```bash
curl -fsSL https://github.com/pax-beehive/memory-adaptor/releases/latest/download/install.sh | bash
```

If `paxm config doctor` says config is missing or invalid, the user needs one interactive setup pass:

```bash
paxm setup
```

`paxm setup` lets the user choose memory providers and agent hooks. Active recall needs at least one enabled readable provider. `sqlite` works without an API key; remote providers such as Zep require the user to provide their own API key during setup. Passive hook recall only works after hooks are installed by setup, but active commands still work independently.

If the user gives a config path, pass it through every command:

```bash
paxm --config /path/to/config.yaml recall --query "..." --json
```

## Active Recall

Before answering a question that depends on prior project decisions, user preferences, repo history, or old debugging context, run a targeted recall:

```bash
paxm recall --query "short concrete query" --limit 3 --json
```

Use a query that captures the user's current task, not the whole prompt. Include stable identifiers when available: repo name, module name, command, error text, feature name, issue id, or decision keyword.

Read scores and provider metadata. Treat memory as supporting context, not source of truth. Verify against the current repo or live system when the fact can drift, is high stakes, or is cheap to check.

If recall returns nothing relevant, continue normally and do not imply memory found evidence.

## Remember

Store only durable, reusable facts:

```bash
paxm remember --text "Decision: paxm setup owns provider and hook configuration; visible hook install/test commands are intentionally omitted."
```

Good candidates:

- user preferences that should affect future agent behavior;
- project architecture decisions and settled terminology;
- commands or fixes that resolved a recurring issue;
- repo-specific conventions that are likely to matter again.

Do not store secrets, API keys, access tokens, private raw logs, large pasted content, or short-lived task state. Ask before storing sensitive personal or business information.

## Debugging Memory Use

Use history to inspect local recall/write activity and provider behavior:

```bash
paxm history --days 7
paxm history --days 7 --json
```

Use this when the user asks whether passive recall fired, why a memory was not recalled, which providers were read or written, or how often agents are using memory.

## Operating Rules

- Keep active recall conservative: do not inject weakly related memories into the answer.
- Prefer `--json` for agent consumption because it includes structured scores and provider fields.
- Keep `--limit` small by default; use `--limit 5` or higher only when the task genuinely needs broader context.
- Do not run `paxm setup` silently. It is interactive and changes user-owned config and hooks.
- Do not edit paxm config files by hand unless the user explicitly asks; prefer `paxm setup` for configuration.
- Do not treat passive hook behavior as guaranteed unless setup installed hooks and history shows events.
