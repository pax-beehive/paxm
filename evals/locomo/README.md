# LoCoMo agent-memory benchmark

The primary LoCoMo benchmark measures whether a real agent performs better when
connected to memory through paxm. It imports the official `locomo10.json`,
isolates each conversation in the selected provider, and runs each question in
three fresh agent sessions:

- `control`: the agent sees only the question;
- `passive`: the agent receives memory through its real paxm lifecycle hook;
- `active`: the agent is instructed to call `paxm_recall` through the real MCP
  server before answering.

Answers are scored deterministically with normalized token F1. The report
includes per-arm accuracy, mean F1, exact match, model tokens and cost, recall
usage, and passive/active lift over control. This score is intentionally not
presented as the official LoCoMo LLM-judge accuracy.

Before scoring each conversation, paxm also runs a write canary through the
same production OpenCode plugin (`session.idle` -> `turn_end` -> paxm capture
queue -> provider), flushes the queue, and verifies that the canary is
searchable. LoCoMo fixtures are then bulk-seeded directly so setup does not add
hundreds of paid agent calls. The plugin source is shared with the production
integration rather than copied into the eval harness.

Download `locomo10.json` from the
[official SNAP Research repository](https://github.com/snap-research/locomo/tree/main/data),
then run:

```bash
paxm --config ~/.config/paxm/config.yaml eval run locomo \
  --dataset /path/to/locomo10.json \
  --agent opencode \
  --model deepseek/deepseek-v4-flash \
  --provider sqlite \
  --max-questions 10 \
  --output locomo-opencode-sqlite.json
```

Agent evaluation makes paid model calls. The CLI requires an explicit
`--model PROVIDER/MODEL` so results are reproducible, plus either
`--max-questions N` or the explicit `--all` acknowledgement. Use `--arms` to
select a subset, for example `--arms control,active`.

The lower-level provider retrieval diagnostic remains available separately:

```bash
paxm eval retrieval locomo \
  --dataset /path/to/locomo10.json \
  --provider sqlite \
  --limit 10
```

Remote providers are fail-closed. Each conversation receives a unique eval
scope, every returned memory ref is recorded in an atomic local manifest, and
cleanup runs after success or failure. Mem0 uses an isolated `run_id` with
inference disabled so evidence IDs survive. Zep uses an isolated graph and
episode search. SQLite uses a disposable database. JSON-RPC providers must be
run with `--keep-memory` until their protocol exposes a reliable cleanup
capability.

Use a settle duration for asynchronously indexed providers:

```bash
paxm eval run locomo --dataset locomo10.json --agent opencode --model PROVIDER/MODEL \
  --provider zep --max-questions 10 --settle 10s
```

Interrupted runs can be recovered from their manifests:

```bash
paxm eval cleanup --run RUN_ID
paxm eval cleanup --stale
```

`--keep-memory` is an explicit debugging escape hatch. An explicit
`eval cleanup --run RUN_ID` later overrides it.

The retrieval diagnostic reports overall and per-category Recall@K,
Precision@K, MRR, per-question hit IDs, and execution failures. It exists to
explain agent failures, not as paxm's primary outcome benchmark.
