# Agent Integrations

`paxm setup` can install active MCP access and native lifecycle hooks for
Cursor, TRAE, TRAE CN, Kimi Code, ZCode, Kiro, and Cline. These integrations
are thin clients over the same paxm config, runtime, memory router, and provider
adapters used by the CLI.

## Capability matrix

| Client | Active recall/write | Passive recall | Passive write |
| --- | --- | --- | --- |
| Cursor | MCP | Not per prompt; use MCP | `beforeSubmitPrompt` + `afterAgentResponse` hooks |
| TRAE | MCP | `UserPromptSubmit` | `SessionStart` + `UserPromptSubmit` + `Stop` |
| TRAE CN | MCP | `UserPromptSubmit` | `SessionStart` + `UserPromptSubmit` + `Stop` |
| Kimi Code | MCP | `UserPromptSubmit` | `SessionStart` + `UserPromptSubmit` + `Stop` |
| ZCode | MCP | `UserPromptSubmit` | `SessionStart` + `UserPromptSubmit` + `Stop` |
| Kiro | MCP | `userPromptSubmit` in the `paxm` agent | `agentSpawn` + `userPromptSubmit` + `stop` in the `paxm` agent |
| Cline | MCP | `UserPromptSubmit` | `TaskStart` + `UserPromptSubmit` + `TaskComplete` |

Cursor's `beforeSubmitPrompt` output contract can allow or block a prompt but
does not inject prompt-specific context. Paxm therefore registers the MCP
server for explicit Cursor recall and uses hooks for passive capture. It does
not claim prompt-time passive recall where the host cannot deliver it.

Cline's `TaskComplete` payload does not include the final assistant response.
Paxm durably captures the task and user-prompt lifecycle that Cline exposes,
but does not synthesize assistant text that the host did not provide.

## Files installed by setup

Setup merges the `paxm` MCP entry and paxm hook commands into existing config.
It preserves unrelated servers, hooks, settings, and top-level JSON fields.

| Client | Native hook config | MCP config |
| --- | --- | --- |
| Cursor | `~/.cursor/hooks.json` | `~/.cursor/mcp.json` |
| TRAE | `~/.trae/hooks.json` | macOS: `~/Library/Application Support/Trae/User/mcp.json` |
| TRAE CN | `~/.trae-cn/hooks.json` | macOS: `~/Library/Application Support/Trae CN/User/mcp.json` |
| Kimi Code | `~/.kimi-code/config.toml` | `~/.kimi-code/mcp.json` |
| ZCode | `~/.zcode/cli/config.json` | same file, under `mcp.servers.paxm` |
| Kiro | `~/.kiro/agents/paxm.json` | `~/.kiro/settings/mcp.json` |
| Cline | `~/.cline/hooks/{TaskStart,UserPromptSubmit,TaskComplete}` | `~/.cline/data/settings/cline_mcp_settings.json` |

Kiro lifecycle hooks belong to the generated custom agent. Start it with:

```bash
kiro-cli chat --agent paxm
```

The global Kiro MCP entry remains available to agents that include user-level
`mcp.json` configuration.

Cline allows one executable at each global hook filename. If a requested hook
file already exists and is not paxm-managed, setup stops with an error instead
of overwriting it. Move or compose that hook explicitly, then rerun setup.

Tests and managed deployments can override paths with:

- `PAXM_CURSOR_HOOKS`, `PAXM_CURSOR_MCP`
- `PAXM_TRAE_HOOKS`, `PAXM_TRAE_MCP`
- `PAXM_TRAE_CN_HOOKS`, `PAXM_TRAE_CN_MCP`
- `PAXM_KIMI_CONFIG`, `PAXM_KIMI_MCP`
- `PAXM_ZCODE_CONFIG`
- `PAXM_KIRO_AGENT`, `PAXM_KIRO_MCP`
- `PAXM_CLINE_HOOKS_DIR`, `PAXM_CLINE_MCP`

`CLINE_DATA_DIR` is also honored when `PAXM_CLINE_MCP` is unset.

## Install, verify, disable, and roll back

Install or upgrade idempotently by rerunning setup and selecting the client:

```bash
paxm setup
paxm config doctor
```

Verify the same storage/runtime path before trusting passive capture:

```bash
paxm remember --profile ltm --text "PAXM_AGENT_INTEGRATION_PROBE"
paxm recall --query "PAXM_AGENT_INTEGRATION_PROBE"
paxm history --days 1
```

Then verify in the real client:

1. Start a fresh client session and confirm the paxm MCP server is healthy.
2. Recall the probe through MCP, or submit a matching prompt on clients with
   passive `UserPromptSubmit` recall.
3. Complete a turn, then check `paxm history --days 1` for the client target.
4. Confirm session ID, workspace, and event order in `paxm logs --tail 100`.

Native hook failures are fail-open at the client boundary: a missing provider,
timeout, or paxm hook error must not block the coding session.

Disable and remove one integration without deleting memory or provider config:

```bash
paxm uninstall --agent cursor --yes
paxm uninstall --agent trae-cn --yes
```

Accepted names include `cursor`, `trae`, `trae-cn` (also `trae cn`), `kimi`
(also `kimi code`), `zcode`, `kiro`, and `cline`. Running uninstall again is
safe. To roll back the paxm binary itself, install a pinned earlier release with
`PAXM_VERSION`; client config remains user-owned and can be re-enabled by
running setup again.
