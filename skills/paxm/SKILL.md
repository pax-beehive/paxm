---
name: paxm
description: Use paxm as an active agent memory layer. Trigger when the user asks to recall prior context, search or inspect memory, remember working state or a durable fact, debug paxm history/metrics, or when a task would benefit from active memory recall before answering, especially repo, project, preference, architecture, or previous-decision questions.
---

# paxm

Use `paxm` to actively recall and record memory while keeping setup and provider policy under the user's control. Active agent writes should use short-term memory (`stm`) unless the user or evidence clearly calls for durable long-term memory (`ltm`).

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

`paxm setup` lets the user choose memory provider instances and passive agent integrations for Codex, Claude Code, or Pi. It does not install active recall skills; the user owns that installation. Active recall needs at least one enabled readable provider instance. `sqlite` works without an API key; remote providers such as Zep or Mem0 require the user to provide their own connection details during setup, and JSON-RPC plugin providers require a local plugin command. Passive hook recall only works after hooks are installed by setup, but active commands still work independently. Passive hook writes use long-term memory (`ltm`) by default; active agent writes should normally use `stm`.

If the user gives a config path, pass it through every command:

```bash
paxm --config /path/to/config.yaml recall --query "..." --json
```

## MCP Mode

When the host has paxm configured as an MCP server, prefer the structured MCP
tools over shelling out:

- `paxm_recall` instead of `paxm recall --json`
- `paxm_remember` instead of `paxm remember`; pass `profile: "stm"` for working memory or `profile: "ltm"` for durable facts
- `paxm_history` instead of `paxm history --json`
- `paxm_config_doctor` instead of `paxm config doctor --json`

The server command is:

```bash
paxm mcp serve
```

For a custom config:

```bash
paxm --config /path/to/config.yaml mcp serve
```

MCP mode follows the same operating rules as CLI mode. Do not use it to run
setup, install hooks, uninstall integrations, or backfill old sessions.

## Active Recall

Before answering a question that depends on prior project decisions, user preferences, repo history, or old debugging context, run a targeted recall:

```bash
paxm recall --query "short concrete query" --limit 3 --json
```

Use a query that captures the user's current task, not the whole prompt. Include stable identifiers when available: repo name, module name, command, error text, feature name, issue id, or decision keyword.

Read scores and provider metadata. Treat memory as supporting context, not source of truth. Verify against the current repo or live system when the fact can drift, is high stakes, or is cheap to check.

If recall returns nothing relevant, continue normally and do not imply memory found evidence.

### Multi-Hop Recall

Use multi-hop recall when the first results expose a more precise lead than the
original query: a repo/module name, document title, issue id, symbol, command,
error text, provider metadata, or decision keyword.

Workflow:

1. Run an initial focused recall with a small limit.
2. Read the highest-scoring hits and extract one or two concrete follow-up
   terms that were not in the original query.
3. Run another recall for each useful follow-up term, keeping the query short
   and specific.
4. Merge the evidence across hops, then verify against the current repo or live
   system when the fact can drift or direct verification is cheap.

Example:

```bash
paxm recall --query "Roundtable translation language preference" --limit 3 --json
paxm recall --query "content_translations translation_jobs DeepSeek provider" --limit 3 --json
```

Do not run open-ended recall loops. Stop after 2-3 hops, when a hop returns no
new concrete lead, or when the next step should be repo or web verification
instead of more memory search. Do not treat a follow-up hit as confirmed truth
without checking current source when accuracy matters.

## Remember

Use short-term memory for active task state that may help the next few turns or
near-future follow-ups:

```bash
paxm remember --profile stm --text "Working note: PR #42 is blocked on the mem0 tier-filter test."
```

Use long-term memory only for durable, reusable facts:

```bash
paxm remember --profile ltm --text "Decision: paxm setup owns provider and hook configuration; visible hook install/test commands are intentionally omitted."
```

Paxm consolidates exact repeated LTM text when no explicit ID is supplied. This
does not make semantic duplicates or conflicting statements safe: keep durable
memories concise and consistently worded, and verify current source before
relying on facts that may drift.

Good `ltm` candidates:

- user preferences that should affect future agent behavior;
- project architecture decisions and settled terminology;
- commands or fixes that resolved a recurring issue;
- repo-specific conventions that are likely to matter again.

Do not store secrets, API keys, access tokens, private raw logs, or large pasted content. Do not promote short-lived task state to `ltm`; keep it in `stm`. Ask before storing sensitive personal or business information.

## Debugging Memory Use

Use history to inspect local recall/write activity and provider behavior:

```bash
paxm history --days 7
paxm history --days 7 --json
```

Use this when the user asks whether passive recall fired, why a memory was not recalled, which providers were read or written, or how often agents are using memory.

## Operating Rules

- Keep active recall conservative: do not inject weakly related memories into the answer.
- Use `stm` for active scratchpad-like writes and `ltm` only for durable facts.
- Prefer `--json` for agent consumption because it includes structured scores and provider fields.
- Keep `--limit` small by default; use `--limit 5` or higher only when the task genuinely needs broader context.
- Do not run `paxm setup` silently. It is interactive and changes user-owned config and hooks.
- Do not run `paxm uninstall` silently. It removes passive agent integrations; use `--agent` to scope cleanup and `--yes` only with explicit user approval.
- Do not edit paxm config files by hand unless the user explicitly asks; prefer `paxm setup` for configuration.
- Do not treat passive hook behavior as guaranteed unless setup installed hooks and history shows events.
- Do not look for or run a manual memory cleanup command. Expired STM cleanup is a best-effort hook-flush side effect managed by the hook daemon, and recall filters expired rows even before storage cleanup runs.
