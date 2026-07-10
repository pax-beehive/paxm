# paxm TODO

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
  hooks/hooks.json
  hooks/paxm-hook.sh
  scripts/install-paxm.sh
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

Non-goals:

- Do not replace the standalone `paxm` CLI with a plugin-only runtime.
- Do not silently install binaries, mutate user hook config, or write provider
  credentials during plugin install.
- Do not vendor every platform binary inside the plugin unless Codex plugin
  packaging later provides a clean platform-specific binary distribution path.

Open design questions:

- Whether the plugin should be repo-local first (`.agents/plugins/marketplace.json`)
  or personal/global first (`~/.agents/plugins/marketplace.json`).
- Whether `paxm` should support an explicit plugin data install location such as
  `${PLUGIN_DATA}/bin/paxm` in addition to normal `PATH` lookup.
- Whether the plugin should register the existing `paxm mcp serve` command for
  users automatically after MCP trust review.
