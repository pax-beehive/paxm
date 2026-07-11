# SQLite baseline

This version 1 retrieval suite contains 100 deterministic cases: ten sanitized
topic variants across ten recall categories. Categories assert exact and
partial matching, active and passive paths, ranking order, result limits,
expired-memory suppression, and mixed STM/LTM retrieval. Every case includes
normalized historical turns, expected memories, and a forbidden distractor.
The runner creates an isolated SQLite database for each case and calls the
production runtime and facade.

A case passes when all required memories are present, no explicitly forbidden
memory is returned, and its order/count assertions hold. Precision and the
unexpected-hit rate remain descriptive metrics, so a passing baseline can
still expose retrieval noise worth improving.

Run it with:

```sh
paxm eval run --suite evals/baseline
paxm eval run --suite evals/baseline --json
```

Regenerate `suite.json` after intentionally changing the source matrix:

```sh
go run ./evals/baseline/generate
```
