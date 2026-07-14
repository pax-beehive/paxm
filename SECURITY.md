# Security policy

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability. Use GitHub's
private vulnerability reporting for this repository. If that option is not
available, contact the maintainers through the security contact published on
the PAX organization profile and ask for a private reporting channel before
sharing details.

Include the affected version, platform, agent integration, reproduction steps,
and potential impact. Remove API keys, provider credentials, memory content,
workspace paths, and raw private logs from the report.

We will acknowledge a report as soon as practical, validate its scope, and
coordinate remediation and disclosure with the reporter. Confirmed fixes follow
the normal reviewed release process unless an embargoed security release is
required.

## Supported versions

Security fixes target the latest stable paxm release and the latest tagged
Codex and Claude Code plugins. Users should update to the newest compatible
release before reporting an issue that may already be fixed.

## Security boundaries

- Provider credentials and configuration remain user-owned.
- Hooks and plugins require the host agent's normal trust or installation flow.
- Passive hook failures are fail-open and must not prevent the agent from
  running.
- Local telemetry is intended for local inspection and must not contain raw
  memory queries by default.
- Release archives are accompanied by `SHA256SUMS`; the installer and updater
  verify the selected archive before replacing the binary.
