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
- Generic CRUD/action access to restricted resources such as tokens, users,
  spaces, uploaded files, logs, storage/sync or AI provider configuration.
- Unbounded tool output or upstream error bodies that can overwhelm or confuse an
  agent context.
- Incorrect `outcome_unknown` / `not_attempted` classification for mutating
  requests after timeout, cancellation, retry or circuit-breaker paths.

## Security invariants

- HTTP mode fails closed: non-loopback binds require `TANDOOR_MCP_TOKEN`, short
  tokens are refused for non-loopback binds, and non-loopback cleartext is refused
  unless `TANDOOR_HTTP_ALLOW_CLEAR=true`.
- `set_recipe_image` accepts exactly one source. Local files must be regular
  files inside `TANDOOR_IMAGE_DIR`; URL images must be public `http`/`https`
  URLs without credentials, localhost, private, link-local or internal hosts;
  inline base64 images are capped at 8 MiB decoded and must match PNG/JPEG/WebP
  magic bytes.
- `import_recipe_from_url` and URL image uploads are server-side Tandoor fetches
  after MCP-side URL validation. Failures should identify whether the MCP request
  reached Tandoor and avoid implying the MCP server fetched the remote image.
- Generic resources are denylisted for secret/admin/raw-log surfaces. Generic
  mutations are allowlisted only for low-risk metadata resources. `tandoor_action`
  is a small path allowlist, not a raw URL proxy.
- Error truncation is for agent safety and context size, not privacy. Do not send
  secrets, live personal recipe data, bearer headers or captured private payloads
  into test fixtures, docs, issues or commits.

## Local guardrails

Run these before committing security-sensitive changes:

```sh
make verify
make secret-scan
```

`make install-hooks` installs the tracked `git-secrets` pre-commit hook for this
checkout.

## Supported versions

This is a pre-1.0 project; only the latest tagged release is supported.
