# Cross-agent incident-transfer eval

This experimental harness measures whether experience passively written from a
Pi session helps a fresh Claude Code session avoid the same engineered failure.
It is a real-agent benchmark, not part of deterministic CI.

For every scenario the runner:

1. gives Pi an isolated workspace and requires it to encounter, diagnose, and
   solve a harmless local failure;
2. renders the Pi user/assistant turn through the production
   `HookWriteItem`/`IngestBatch` path into one scenario-local SQLite database;
3. deletes the Pi workspace;
4. runs fresh Claude Code workspaces for `control`, first-session `passive`, and
   `active` arms, deleting each workspace before starting the next;
5. scores task success, trap avoidance, their conjunction (`safe_success`),
   recall hits, sandbox audit, and duration.

## Isolation contract

Pi and Claude share one logical channel per scenario: the SQLite provider owned
by the runner. The database file is never mounted into an agent workspace.
Agents receive only their prompt, their current disposable workspace, and, for
the assisted arms, the exact recall text selected by paxm.

Every agent process and its children run under a macOS Seatbelt profile. The
profile blocks the repository and scenario source, experiment artifact root
(SQLite, config, producer logs, reports), global paxm data, and all local
Pi/Claude/Codex histories. Pi receives a private, sanitized copy of only its
provider/model/auth configuration. The runner reads Claude's existing OAuth
token outside the sandbox and injects it only into that invocation's process;
the token is never logged or written to SQLite. Claude uses a fresh empty
configuration directory, so no user customizations are loaded. File writes are denied globally except for that invocation's
disposable workspace, private runtime directory, and Claude's workspace-derived
scratch child. Existing Claude scratch siblings are explicitly unreadable, so
one arm cannot inspect another arm's scratch data or leave a side-channel.
Before each model call the runner uses the same profile to verify that the
artifact root cannot be read or written and that the isolated workspace remains
writable.

The task executables are compiled by the runner. Agent workspaces contain only
the executable and a non-revealing task note, not source code or a runbook.

## Run

Running this benchmark makes paid real-model calls and requires macOS
`sandbox-exec`, Pi, and Claude Code:

```sh
go run ./evals/cross-agent/run
go run ./evals/cross-agent/run --only deploy-environment
```

Artifacts and `report.json` are written to a fresh `/tmp` directory unless
`--root` is supplied. A small number of trials is a tracer result, not a stable
probability estimate; repeat scenarios before drawing product conclusions.
