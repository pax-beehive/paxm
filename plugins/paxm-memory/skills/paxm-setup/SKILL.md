---
name: paxm-setup
description: Guide a user through first-time paxm setup for the Codex memory plugin, including binary installation, provider configuration, hook trust, and a real smoke test.
---

# Set up paxm for Codex

Run this workflow only when the user asks to configure, repair, or verify paxm.
Do not silently install a binary, write provider credentials, or modify Codex
hooks.

## 1. Diagnose first

Run the read-only checks:

```bash
paxm version
paxm config doctor --json
```

If `paxm` is not on `PATH`, check the standard user install locations before
offering installation. Explain that the installer downloads the latest release
binary by default and ask for explicit approval before running:

```bash
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
```

When a reproducible install or rollback is required, export a compatible
release before running the command:

```bash
export PAXM_VERSION=v0.1.20
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash
```

After installation, run `paxm version` again and report the resolved path.
The equivalent bundled helper is `scripts/install-paxm.sh` when the plugin's
absolute install path is available; do not assume `PLUGIN_ROOT` exists in a
skill shell environment.

## 2. Configure the plugin-owned integration

Ask the user to choose providers and passive behavior, then run the interactive
setup with Codex plugin ownership:

```bash
paxm setup --integration codex-plugin
```

The equivalent bundled helper is `scripts/setup-paxm.sh` when its absolute
plugin path is available. This keeps provider credentials and profiles in paxm while preventing paxm from
registering a duplicate global Codex hook. The plugin's own hook definitions
remain subject to Codex's normal `/hooks` review and trust flow.

## 3. Verify the loop

After setup:

1. Run `paxm config doctor`.
2. Write one random test fact to STM and immediately recall it.
3. Explain which provider answered and that the test fact will expire.
4. Ask the user to start a fresh Codex task or submit one prompt so the passive
   hook can be observed.
5. Use `paxm history --days 1` to confirm the hook event.

If a check fails, identify the layer (plugin, binary, config, provider, hook
trust, or Codex task lifecycle) and give one next action. Do not reinstall
everything by default.

## Safety rules

- Never print or echo provider API keys.
- Never modify `~/.codex/config.toml` from a setup script.
- Never bypass Codex hook trust.
- Hook failures must not block the user's Codex task.
- Keep MCP optional until the binary and plugin-owned hook path are healthy.
