# SQLite retrieval challenge

These deterministic suites define the quality frontier for paxm's default local
provider. They deliberately target lightweight, explicit, rg-like retrieval
rather than embeddings or hidden model-based query expansion.

`suite.json` covers conservative English morphology, a separately identified
bounded product-alias vocabulary, CJK substring retrieval, identifiers in both
split directions (including paths, versions, and error codes), strict all-term
suppression, relaxed fallback when no all-term result exists, and suppression of
long transcript-like distractors. Every substring and identifier case includes a
near distractor so broad matching alone cannot pass.

`workspace-suite.json` is separate because workspace isolation is a correctness
property, not an aggregate relevance tradeoff. Its target budget requires every
case to pass with zero false positives.

The main challenge, workspace isolation, and existing 100-case baseline are all
hard CI quality gates.

On 2026-07-12, the lightweight analyzer first raised the main challenge from 8
to 24 passing cases. Explicit strict-first planning then raised it to 32 of 32,
with Recall@K, Precision@K, and MRR all 1.000 and zero false positives. The
workspace suite passes 5 of 5 with zero false positives, and the existing
baseline passes 100 of 100 with the same perfect metrics.

Run the challenges:

```sh
paxm eval run --suite evals/sqlite-retrieval/suite.json --gate quality \
  --budget evals/sqlite-retrieval/target-budget.json
paxm eval run --suite evals/sqlite-retrieval/workspace-suite.json --gate quality \
  --budget evals/sqlite-retrieval/workspace-target-budget.json
```

Regenerate both suites only after intentionally changing the source matrix:

```sh
go run ./evals/sqlite-retrieval/generate
```

CI enforces all three graduated gates:

```sh
paxm eval run --suite evals/baseline --gate quality \
  --budget evals/baseline/budget.json
paxm eval run --suite evals/sqlite-retrieval/suite.json --gate quality \
  --budget evals/sqlite-retrieval/target-budget.json
paxm eval run --suite evals/sqlite-retrieval/workspace-suite.json --gate quality \
  --budget evals/sqlite-retrieval/workspace-target-budget.json
```
