# paxm Roadmap

This roadmap prioritizes proving that paxm produces useful memory before adding
more integrations or policy controls. The product should remain a small,
provider-neutral memory runtime with human-owned setup and agent-facing recall.

## Product Goal

Make the complete memory loop easy to install, measurable, and trustworthy:

```text
agent activity -> memory write -> provider storage -> later recall -> useful context
```

The next releases should answer three questions in order:

1. Can a new user install paxm and complete the loop without assembling several
   integration pieces manually?
2. Does the loop recall the right memory at the right time without excessive
   false positives?
3. Can the user see, understand, and control what paxm is doing?

## Guiding Principles

- Keep the CLI and shared Go runtime as the product core.
- Keep provider credentials, hook installation, and policy choices under user
  control.
- Measure recall quality before adding more ranking or facade configuration.
- Add agent and provider integrations in response to demonstrated demand, not
  to maximize the integration count.
- Build one native macOS application when visual management is ready. Do not
  add a separate local web UI or a `paxm ui` surface first.
- The macOS application must reuse paxm's runtime and policy behavior rather
  than implement a second router, provider registry, or configuration model.

## Phase 1: Distribution and First Success

Status: completed on 2026-07-10. The clean-install and real-task evidence is in
[`docs/acceptance/phase-1-v0.1.md`](acceptance/phase-1-v0.1.md).

Build the lightweight Codex plugin described in `docs/todo.md` as the first
user-facing distribution path.

Scope:

- Bundle the paxm skill and trusted hook wrappers.
- Detect whether the paxm binary is installed and guide the user through the
  existing release installer when it is missing.
- Keep provider credentials and integration choices in `paxm setup`.
- Run a real remember-and-recall smoke test after setup.
- Report provider health and hook status in language a user can act on.
- Preserve explicit trust review for hooks and MCP registration.

Exit criteria:

- A new Codex user can reach a successful write and recall from a clean machine
  without manually copying skill or hook files.
- Setup failures identify the failing layer: binary, config, provider, hook, or
  agent integration.
- The smoke test proves the configured provider path rather than only checking
  that files exist.

### v0.1 Implementation Slice

The first plugin release is a Codex distribution and onboarding layer, not a
second memory runtime. The repository implementation lives under
`plugins/paxm-memory/` and contains:

```text
plugins/paxm-memory/
  .codex-plugin/plugin.json
  skills/paxm/SKILL.md
  skills/paxm-setup/SKILL.md
  hooks/hooks.json
  hooks/paxm-hook.sh
  scripts/install-paxm.sh
  scripts/setup-paxm.sh
```

The intended first-use journey is:

```text
install plugin
  -> start a new Codex task
  -> run read-only paxm diagnosis
  -> explicitly approve binary installation if needed
  -> run paxm setup --integration codex-plugin
  -> write and recall one expiring STM smoke-test item
  -> review/trust plugin hooks in Codex
  -> verify the first passive event in paxm history
```

The plugin must never silently install the paxm binary, write provider
credentials, or bypass Codex hook trust. Its hooks are fail-open: a missing
binary, invalid config, provider failure, or timeout must not block a Codex
task. The installer and setup scripts are explicit user-invoked actions, not
plugin lifecycle side effects.

Codex loads matching hooks from multiple sources concurrently. To prevent the
plugin and legacy paxm setup from both firing, v0.1 adds an integration-owner
field to the paxm agent config. `paxm setup --integration codex-plugin` records
plugin ownership, removes old paxm-managed Codex registrations, and leaves
provider routing and recall policy in paxm. Plugin hooks identify themselves
with `PAXM_INTEGRATION_OWNER=codex-plugin`; paxm ignores a plugin hook until
plugin ownership is configured and ignores legacy paxm hooks after ownership is
handed to the plugin.

The first plugin release deliberately does not bundle MCP configuration. Skill
and hook startup must remain healthy when paxm is not installed yet. MCP can be
enabled later as an optional path after the binary bootstrap and hook ownership
flow are stable.

v0.1 acceptance checks:

- plugin manifest validation passes;
- hook and setup scripts pass shell syntax validation;
- `paxm setup --integration codex-plugin` is idempotent and does not create a
  Codex global hook;
- the normal paxm-owned setup path remains backward-compatible;
- a real STM write/recall smoke test and a later passive hook event are both
  observable through paxm history;
- full Go tests, `go vet`, and plugin validation pass.

### Current v0.1 Distribution Pairing

The current pairing is:

```text
paxm binary: v0.1.20
plugin:     v0.1.4
marketplace: pax-agent-nexus
```

The plugin installer follows the latest GitHub binary release by default so
plugin installs receive compatible binary fixes without a source update. An
operator may set `PAXM_VERSION` explicitly for reproducible installs, staged
upgrades, or rollback. Binary releases remain gated by the full test and
release-asset workflow before they become `latest`.

The repo-scoped marketplace lives at
`.agents/plugins/marketplace.json` and points at `./plugins/paxm-memory`. The
release path is PR review first, then merge, binary release/tag publication, and
plugin installation from the tagged repository source. An official curated
directory submission is deferred until clean-machine and real Codex task tests
show that installation, hook trust, upgrade, disable, and rollback are reliable.

## Phase 2: Recall Evaluation Harness

Status: deterministic contract/regression layer implemented.
`evals/baseline/suite.json` contains 100 versioned SQLite retrieval cases, and
`evals/conversation-write/suite.json` contains 50 cases with normalized
conversation history and hook messages that execute production write, ingest,
and later recall paths, including passive recall-envelope and active recall-tool
echo suppression. `evals/lifecycle/suite.json` adds 40 cases covering
active/passive recall after runtime restart, duplicate-write consolidation, and
recall-echo suppression. `paxm eval run` can save and compare compatible result
files. Provider-quality budgets remain available for opt-in benchmarking; CI
uses the adapter contract gate instead.

Paxm's hard quality gate is adapter fidelity, not provider retrieval quality.
The eval gate checks that visible content and metadata cross the write boundary
intact, that recall echoes and hidden reasoning are removed as specified, and
that writes are acknowledged. Go contract tests with capture providers cover
recall request forwarding, result mapping, routing, and failure/recovery policy.
Recall@K, MRR, semantic ranking, and provider consolidation quality remain
observable benchmark metrics and do not block paxm changes.

An opt-in cross-agent tracer also exists under `evals/cross-agent`. It runs Pi
producer sessions and fresh Claude Code control/passive/active consumers in
audited OS sandboxes, with one SQLite database as the only shared channel. The
initial three-scenario run showed 2/3 safe success for control and 3/3 for both
memory-assisted arms. This is directional evidence, not a probability estimate;
scenario expansion is not part of the remaining paxm roadmap.

The evaluation and adapter contract layer is complete and exercises the same
runtime, facade, router, profiles, and provider adapters used in production.

### Provider Adapter Contract Matrix

Status: completed. SQLite, Mem0, Zep, and JSON-RPC run the same shared contract
harness with provider-specific fixtures. The matrix covers stable naming,
health behavior, write acknowledgements, search result identity, and the common
provider boundary and context cancellation. Existing provider-specific tests
supplement the shared matrix with backend request shapes and supported response
fields; their capabilities are not assumed to be identical.

The executable matrix and its scope are documented in
[`docs/provider-adapter-contract.md`](provider-adapter-contract.md). It
deliberately excludes ranking, semantic recall, consolidation quality, latency,
and result counts.

With the adapter contract matrix, durable queue tests, telemetry contracts, and
agent write/cleaning gate in CI, Phase 2 is complete. No additional provider
quality or challenge-set work is required for the paxm roadmap.

## Phase 3: Native macOS Application

Status: product and interaction design in progress in a separate Claude design
track. Implementation should begin only after that design is reviewed against
the runtime boundaries below.

Build a single native SwiftUI application suitable for normal desktop use and
an optional menu bar entry.

The application should be a client of the paxm core. Provider calls, routing,
ranking, admission, telemetry, and config validation remain in Go. Any new
machine-facing interface required by the application should be narrow and
shared with other paxm entrypoints where practical.

### 3A: Read-only Observer

- Provider and hook health with actionable diagnostics.
- Daemon and durable queue status, including pending, retry, and failed writes.
- A timeline of memory writes, recall attempts, inserted results, and failures.
- Provider latency, queue delay, routing outcome, and error details.
- Clear disclosure of where memory is stored and which providers receive each
  write.

### 3B: Diagnostics and Safe Actions

- Provider health checks and hook smoke tests.
- Flush or retry durable pending writes without bypassing queue policy.
- Manual remember and active recall through the Go runtime.
- Open logs/config locations and export a bounded diagnostic report.

### 3C: Controlled Configuration

- Enable or disable providers and choose required versus best-effort routing.
- Configure agent passive write/recall and provider concurrency.
- Offer intent presets only when they map cleanly to existing Go policy.
- Validate every proposed change and run a smoke test after applying it.

Swift must not edit YAML directly. Add a narrow typed Go interface for config
reads, validation, and updates. Any presets, explanations, or migrations needed
by the application belong in this slice rather than a separate facade phase.

### 3D: Distribution

- Signing and notarization.
- First-run binary installation and hook-trust guidance.
- Application and paxm binary update behavior, disable, and rollback.
- Optional menu bar status after the main application is reliable.

### Boundaries

- Do not add a browser-hosted local dashboard or `paxm ui` command as an
  intermediate product.
- Do not duplicate YAML parsing or facade policy in Swift.
- Do not expose arbitrary memory deletion or destructive bulk editing until the
  provider lifecycle contract can express and verify those operations safely.
- Do not make the GUI a requirement for CLI, MCP, hook, or headless use.

Exit criteria:

- A macOS user can install, configure, verify, inspect, remember, and recall
  through one application.
- An event shown in the application can be traced to the profile, provider, and
  policy decision that produced it.
- CLI and application behavior agree for the same config and query.

## Phase 4: Targeted Ecosystem Expansion

Expand only where usage evidence identifies a meaningful gap.

### Agent Support

MCP already provides a general active-recall path. Add an agent-specific
integration when passive lifecycle hooks or distribution materially improve the
experience. Each new agent integration must include a real end-to-end test that
proves write and recall through the agent's actual runtime.

Claude Code plugin status: released and supported as of paxm v0.1.17. The repo
marketplace publishes `paxm-claude` version 0.1.17 with the paxm and paxm-setup
skills, all five existing Claude lifecycle hooks, and `paxm mcp serve`. Users
install it with:

```text
claude plugin marketplace add pax-beehive/memory-adaptor
claude plugin install paxm-claude@pax-memory --scope user
paxm setup --integration claude-plugin
```

Setup migrates integration ownership and removes only legacy paxm-managed
Claude hooks. Release acceptance used the real Claude Code runtime and proved
passive write to SQLite and Zep, fresh-session passive recall without tool use,
explicit active recall through the paxm MCP tool, marketplace installation,
disable, and re-enable behavior. The release also fixed episode integrity
verification for captured events carrying admission text.

OpenCode is the first integration candidate to investigate:

- active recall already works through OpenCode's local MCP server support, so
  document that path before adding agent-specific code;
- OpenCode plugins expose message, tool, and session lifecycle events including
  `session.idle`, which may support passive write without transcript scraping;
- run a small real-runtime spike to verify event payload completeness, session
  identity, workspace identity, ordering, and fail-open behavior;
- ship a dedicated plugin only if it materially improves passive write or
  passive recall over MCP alone, and require an audited end-to-end test.

### Provider Support

Prefer strengthening the JSON-RPC provider boundary over accumulating adapters
in core:

- publish the provider protocol and lifecycle expectations;
- provide a conformance test kit and sample provider;
- document relevance normalization and idempotency behavior;
- add a built-in provider only when it serves a common use case that the plugin
  boundary cannot serve cleanly.

## Current Priority Order

1. Review the in-progress macOS design, then implement Phase 3A.
2. Continue through 3B-3D without duplicating the Go runtime in Swift.
3. Run an OpenCode integration spike; ship only if MCP plus plugin lifecycle
   events provide a clear passive-memory improvement.
4. Expand other agents or providers only from demonstrated demand.
