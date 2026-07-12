# paxm for Claude Code

This plugin packages paxm skills, five Claude lifecycle hooks, and the paxm MCP
server. Provider credentials and policy remain in the standalone paxm config.

```sh
claude plugin marketplace add pax-beehive/paxm
claude plugin install paxm-claude@pax-memory
paxm setup --integration claude-plugin
```

The setup migration removes only legacy paxm-managed Claude hooks and records
`claude-plugin` ownership. Other Claude hooks are preserved. Plugin hooks are
fail-open when the binary, config, or provider is unavailable.
