# LoCoMo agent-memory benchmark

## Exploratory SQLite result

On 2026-07-13, a balanced 30-question run from LoCoMo conversation `conv-26`
produced the following active-recall result:

| Memory arm | Successful answers | Mean token F1 | Exact match |
| --- | ---: | ---: | ---: |
| paxm SQLite turn memory | 13 / 30 | 0.4211 | 13.3% |
| Mem0 product-default | 11 / 30 | 0.3811 | 13.3% |

The answering runtime was OpenCode `1.17.18` with DeepSeek V4 Flash. Both arms
used the real paxm MCP recall seam. SQLite used no external model for memory
ingestion or retrieval.

Mem0 product-default used GPT-5 mini extraction, OpenAI embeddings, and
Postgres with pgvector. The SQLite run used completed dialogue turns as its
ingestion unit.

Success means normalized token F1 was at least `0.5`. This deterministic score
is not the official LoCoMo LLM-judge metric. The Mem0 result is a reference
from the same question set, not a simultaneous deterministic replay.

The SQLite run had one OpenCode database-lock error. A serial retry completed
but remained below the success threshold, so the clean result stayed 13 / 30.

LoCoMo fixtures were seeded directly through paxm. The run evaluates turn-level
retrieval, while a separate canary validates the production capture path. It
does not treat fixture ingestion as proof of every hook metadata field.

This small result supports describing SQLite as competitive in an initial
agent evaluation. It does not establish broad superiority: one conversation,
30 questions, and stochastic agent output are not enough for that claim.

The next publishable milestone is 3–5 conversations and 100–150 paired
questions, with equal context-token budgets, bootstrap confidence intervals,
and category-level reporting.

## Methodology

The primary LoCoMo benchmark measures whether a real agent performs better when
connected to memory through paxm. It imports the official `locomo10.json` and
isolates each conversation in the selected provider.

Each question runs in three fresh agent sessions:

- `control`: the agent sees only the question;
- `passive`: the agent receives memory through its real paxm lifecycle hook;
- `active`: the agent is instructed to call `paxm_recall` through the real MCP
  server before answering.

Answers are scored deterministically with normalized token F1. The report
includes accuracy, mean F1, exact match, model tokens and cost, recall usage,
and memory lift over control.

Token F1 is intentionally not presented as official LoCoMo LLM-judge accuracy.

Before scoring each conversation, paxm also runs a write canary through the
same production OpenCode plugin (`session.idle` -> `turn_end` -> paxm capture
queue -> provider). It flushes the queue and verifies that the canary is
searchable.

LoCoMo fixtures are then bulk-seeded directly, avoiding hundreds of setup model
calls. The eval harness uses the production plugin source rather than a copy.

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
`--model PROVIDER/MODEL`, plus either `--max-questions N` or the explicit
`--all` acknowledgement.

Use `--arms` to select a subset, for example `--arms control,active`.

The lower-level provider retrieval diagnostic remains available separately:

```bash
paxm eval retrieval locomo \
  --dataset /path/to/locomo10.json \
  --provider sqlite \
  --limit 10
```

Remote providers are fail-closed. Each conversation receives a unique eval
scope, every returned memory ref is recorded in an atomic local manifest, and
cleanup runs after success or failure.

Mem0 uses an isolated `run_id` with inference disabled so evidence IDs survive.
Zep uses an isolated graph and episode search. SQLite uses a disposable
database.

JSON-RPC providers require `--keep-memory` until their protocol exposes a
reliable cleanup capability.

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
