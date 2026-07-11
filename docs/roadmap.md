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

### v0.1 Distribution Pairing

The first public-compatible pairing is:

```text
paxm binary: v0.1.12
plugin:     v0.1.0
marketplace: pax-agent-nexus
```

The plugin installer pins `PAXM_VERSION` to `v0.1.12` by default because the
plugin-aware setup flag is not available in the existing `v0.1.11` binary. A
user may override the version explicitly when testing a compatible build.

The repo-scoped marketplace lives at
`.agents/plugins/marketplace.json` and points at `./plugins/paxm-memory`. The
release path is PR review first, then merge, binary release/tag publication, and
plugin installation from the pinned repository source. An official curated
directory submission is deferred until clean-machine and real Codex task tests
show that installation, hook trust, upgrade, disable, and rollback are reliable.

## Phase 2: Recall Evaluation Harness

Build the evaluation harness before starting the macOS application. Its results
will determine which policy controls and explanations the UI actually needs.

The harness should exercise the same runtime, facade, router, profiles, and
provider adapters used in production. It must not contain a separate recall
implementation.

### Scenario Model

Each scenario should contain:

- normalized historical user and assistant turns;
- durable facts or decisions expected to be written;
- one or more later recall queries;
- expected relevant memories;
- known irrelevant or harmful memories that must not be inserted;
- workspace, agent, session, and time metadata when they affect behavior.

Start with a small set of sanitized scenarios derived from real paxm usage.
Cover active recall, initial passive recall, later passive recall, duplicate
writes, STM expiry, LTM consolidation, and multi-provider routing.

### Measurements

At minimum, report:

- recall at K and mean reciprocal rank;
- precision at K and false-positive insertion rate;
- missed expected memories;
- provider and end-to-end latency;
- result count and inserted context size;
- required-provider failures and best-effort degradation;
- results grouped by provider, recall profile, and scenario.

The runner should emit both machine-readable JSON and a concise human-readable
report. A comparison mode should show regressions and improvements between two
profiles, providers, or code revisions.

### Initial Exit Criteria

- The repository has a versioned baseline scenario suite.
- CI can run the deterministic local-provider portion without external API
  keys.
- Remote-provider evaluations are opt-in and clearly separated from the local
  baseline.
- Changes to ranking, thresholds, admission, deduplication, or default profiles
  include before-and-after evaluation evidence.
- Acceptable regression budgets are recorded once the first baseline is known;
  numeric targets should come from measured results rather than being invented
  in advance.

## Phase 3: Native macOS Application

After the evaluation harness establishes the important quality signals, build a
single native macOS application. Prefer a native SwiftUI experience suitable
for normal desktop use and an optional menu bar entry.

The application should be a client of the paxm core. Provider calls, routing,
ranking, admission, telemetry, and config validation remain in Go. Any new
machine-facing interface required by the application should be narrow and
shared with other paxm entrypoints where practical.

### Initial Experience

- Guided setup for agent integrations and providers.
- Provider and hook health with actionable diagnostics.
- Manual memory write and recall.
- A timeline of memory writes, recall attempts, inserted results, and failures.
- Search results showing provider, tier, score, profile, and relevant metadata.
- Recall explanations showing why an item was inserted or filtered, including
  thresholds and policy decisions.
- Safe configuration through opinionated presets with an advanced view for the
  existing profile model.
- Clear disclosure of where memory is stored and which providers receive each
  write.

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

### Provider Support

Prefer strengthening the JSON-RPC provider boundary over accumulating adapters
in core:

- publish the provider protocol and lifecycle expectations;
- provide a conformance test kit and sample provider;
- document relevance normalization and idempotency behavior;
- add a built-in provider only when it serves a common use case that the plugin
  boundary cannot serve cleanly.

## Phase 5: Facade Evolution Driven by Evidence

Do not add facade controls merely because they are configurable. Use evaluation
results and real user failures to decide what belongs in the public model.

Near-term improvements should favor:

- intent-based presets such as local/private, balanced, and remote memory;
- explainability for existing thresholds, weights, tiers, and insertion rules;
- safe comparison of proposed configuration changes against the evaluation
  suite;
- migrations that keep existing configs valid.

Additional ranking algorithms, semantic lifecycle behavior, conflict
resolution, STM promotion, or LTM supersession should be separate decisions with
their own scenarios and measurable acceptance criteria.

## Current Priority Order

1. Codex plugin distribution and first-success verification.
2. Recall evaluation harness and baseline scenarios.
3. Native macOS application using the paxm core.
4. Agent and provider integrations selected from demonstrated demand.
5. Additional facade behavior justified by evaluation evidence.
