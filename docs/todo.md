# paxm TODO

The detailed delivery plan is in [`docs/roadmap.md`](roadmap.md). The v0.1
Codex plugin slice is scaffolded under `plugins/paxm-memory/`. The initial
real-task blocker and successful remediation run are recorded in
[`docs/acceptance/phase-1-v0.1.md`](acceptance/phase-1-v0.1.md). The initially
validated release pairing was binary `v0.1.13` with plugin `v0.1.1`; current
plugin installs follow the latest stable binary release. Phase 1 is complete.
Phase 2 starts with `paxm eval run --suite evals/baseline`: a versioned,
deterministic 100-case SQLite retrieval suite that runs in CI. The second slice,
`paxm eval run --suite evals/conversation-write`, adds 50 deterministic cases
covering production hook writes, visible conversation and tool content,
reasoning suppression, metadata preservation, recall-echo suppression, and
later recall.

The opt-in `evals/cross-agent` tracer measures Pi-to-Claude incident transfer
across control, passive-initial, and active recall arms with isolated workspaces
and a scenario-local SQLite provider as the only shared channel.

## Codex Plugin Distribution

Build a lightweight Codex plugin as the user-facing distribution layer for
paxm. Keep the CLI as the core product and release artifact; the plugin should
bundle Codex-facing setup, skills, and hook wrappers so users do not have to
manually assemble the integration from separate pieces.

Target shape:

```text
paxm-memory/
  .codex-plugin/plugin.json
  skills/paxm/SKILL.md
  skills/paxm-setup/SKILL.md
  hooks/hooks.json
  hooks/paxm-hook.sh
  scripts/install-paxm.sh
  scripts/setup-paxm.sh
  assets/
```

The plugin should:

- bundle the `paxm` skill so new Codex sessions know when and how to run active
  recall, remember durable facts, inspect history, and perform multi-hop recall;
- bundle Codex hook definitions and thin hook wrappers that call `paxm __hook`;
- detect whether `paxm` is installed and guide the user through the existing
  release installer when it is missing;
- keep provider setup in `paxm setup`, where the user explicitly chooses memory
  providers, credentials, and hook behavior;
- use plugin hook trust review instead of trying to silently enable hooks.

The v0.1 implementation uses `paxm setup --integration codex-plugin` to make
plugin hook ownership explicit and remove legacy paxm-managed Codex hook
registrations before the plugin starts handling events.

Non-goals:

- Do not replace the standalone `paxm` CLI with a plugin-only runtime.
- Do not silently install binaries, mutate user hook config, or write provider
  credentials during plugin install.
- Do not vendor every platform binary inside the plugin unless Codex plugin
  packaging later provides a clean platform-specific binary distribution path.

Open design questions:

- The repo-local marketplace is now the first distribution path:
  `.agents/plugins/marketplace.json`. A personal marketplace remains useful for
  private local development but is not the public release contract.
- Whether `paxm` should support an explicit plugin data install location such as
  `${PLUGIN_DATA}/bin/paxm` in addition to normal `PATH` lookup.
- Whether the plugin should register the existing `paxm mcp serve` command for
  users automatically after the binary bootstrap and plugin-owned hook flow are
  stable. MCP is intentionally deferred from v0.1.
