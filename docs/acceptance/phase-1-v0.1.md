# Phase 1 v0.1 Real Acceptance

Date: 2026-07-10

Result: **Passed after remediation**

This run exercised the public Codex distribution path and a real Codex task.
It did not rely only on manifest validation or direct CLI unit tests.

## Release Pairing

```text
paxm binary: v0.1.13
plugin:      v0.1.1
marketplace: pax-agent-nexus
source ref:  paxm-memory-v0.1.1
Codex CLI:   v0.144.1
```

## Passed Checks

- A clean temporary `CODEX_HOME` cloned the public GitHub marketplace from a
  pinned plugin release tag.
- `codex plugin add paxm-memory@pax-agent-nexus` installed and enabled the
  pinned plugin version.
- The bundled installer downloaded and installed the paired Darwin arm64 paxm
  binary into an isolated directory.
- `paxm setup --integration codex-plugin --yes --force` was idempotent, recorded
  plugin ownership, and did not register a duplicate Codex hook.
- The clean SQLite provider passed `paxm config doctor` and completed a real STM
  remember/recall round trip.
- A real Codex task presented the three plugin hooks for review and persisted
  trust after approval.
- Codex displayed the plugin's `Recalling paxm memory` status message and paxm
  telemetry recorded the `codex/user_input` recall with hits inserted.
- Removing and reinstalling the plugin succeeded in the isolated environment.
  The standalone CLI continued to recall the stored SQLite memory while the
  plugin was removed, proving that plugin rollback does not remove memory or
  make the paxm runtime unusable.

## Initial Blocking Failure

The real Codex task rejected the plugin's `UserPromptSubmit` hook output:

```text
UserPromptSubmit hook (failed)
error: hook returned invalid user prompt submit JSON output
```

The plugin wrapper invokes:

```text
paxm __hook --target codex --event user_input --json
```

That command emits paxm's internal `HookResult` JSON (`target`, `event`,
`query`, and `recall`). Codex expects either plain-text additional context or
its documented hook JSON envelope containing
`hookSpecificOutput.hookEventName = "UserPromptSubmit"` and
`hookSpecificOutput.additionalContext`. The JSON is syntactically valid but is
not valid Codex hook output, so the recalled memories never reach the model.

The task consequently answered that no recalled value was available even
though direct recall found the acceptance memory and paxm telemetry showed five
hits selected by the passive hook.

## Additional Finding

The machine already had plugin version `0.1.0` enabled while the first `paxm`
on `PATH` was still `v0.1.9`. That binary predates plugin integration ownership,
so the fail-open hook could silently do no useful work until the plugin setup
workflow upgraded the binary to `v0.1.12`. The setup skill diagnoses this, but
the installed/enabled state alone does not prove that the runtime is compatible.

## Remediation and Re-run

The fix keeps paxm's internal hook result available to machine-facing recall
commands but translates internal Codex `__hook --json` output into the
documented envelope:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "UserPromptSubmit",
    "additionalContext": "Relevant memory recalled by paxm: ..."
  }
}
```

The no-hit path now exits successfully without writing stdout. Regression tests
cover both the native envelope and the silent no-hit behavior while preserving
the existing raw JSON contract for `recall --hook-event --json` and non-Codex
targets.

The real Codex task was repeated with the fixed binary. Codex reported the
`UserPromptSubmit` hook as completed, displayed the recalled hook context, and
the model returned `plugin recall path is healthy` without calling a tool or
running a shell command. This closes the passive-injection blocker.
