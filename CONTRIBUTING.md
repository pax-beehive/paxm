# Contributing to paxm

Thanks for helping make durable agent memory easier to install, trust, and
operate.

## Start with the right channel

- Use GitHub Discussions for setup help, questions, early ideas, and usage
  examples.
- Open a bug report when behavior is reproducibly incorrect.
- Open an integration request when proposing a new agent or provider.
- Report security issues privately according to [SECURITY.md](SECURITY.md).

For a substantial change, start with an issue or discussion before writing the
implementation. This keeps public surfaces small and confirms that the change
fits paxm's provider-neutral runtime.

## Development workflow

1. Fork the repository and create a focused branch from the latest `main`.
2. Keep CLI code limited to parsing and output. Runtime behavior belongs behind
   facade/tools, the memory router, and provider adapters.
3. Add or update tests for behavior changes.
4. Update README or `docs/` when behavior, configuration, architecture, or the
   roadmap changes.
5. Open a pull request that explains the user outcome and validation performed.

Run the full local gate before requesting review:

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint fmt --diff
golangci-lint run
scripts/check-production-complexity.sh
go test ./... -covermode=atomic -coverprofile=coverage.out
scripts/check-production-coverage.sh coverage.out 75
```

Use Go 1.25. Paid cross-agent evaluations are opt-in and are not required for
ordinary pull requests.

## Design boundaries

- Setup, configuration, installation, logs, and diagnostics are operator
  capabilities. Agent-facing recall and remember tools stay narrow.
- Provider integrations use the existing registry, config, and adapter seams.
  The CLI must not depend directly on a concrete provider.
- Passive hooks fail open and must never block the host agent.
- Users retain control of credentials, hook trust, installation, data location,
  disable, upgrade, and rollback.
- Do not include secrets, private logs, credentials, or user memory in issues,
  tests, commits, or pull requests.

Small, explainable pull requests are easier to review and release safely.
