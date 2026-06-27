# Security Policy

## Reporting a vulnerability

Please report security issues **privately**, not via public issues or pull
requests.

- Preferred: GitHub's [private vulnerability reporting](https://github.com/khr4/tandoor-mcp/security/advisories/new)
  (Security → Advisories → Report a vulnerability), once enabled.
- Otherwise: open a minimal issue asking for a private contact channel, without
  details.

Please include reproduction steps and the affected version/commit. You can expect
an initial response within a few days.

## Scope

This server holds a Tandoor API token and, in HTTP mode, can be reached over the
network. Findings of particular interest:

- Leakage of the configured `TANDOOR_TOKEN` or the HTTP `TANDOOR_MCP_TOKEN`.
- Authentication bypass on the HTTP transport (`/mcp`, `/sse`).
- SSRF, path traversal, or arbitrary file read via tool inputs
  (e.g. `set_recipe_image`, `import_recipe_from_url`, `tandoor_action`).
- Any way to reach a non-loopback listener without the bearer token.

## Supported versions

This is a pre-1.0 project; only the latest tagged release is supported.
