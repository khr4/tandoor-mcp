# tandoor-mcp

An MCP server for [Tandoor Recipes](https://tandoor.dev). It runs over stdio and
exposes **task-oriented tools designed for agents** — find/read/create recipes,
plan meals, manage the shopping list and pantry — that hide Tandoor's nested REST
shapes (names instead of ids, parsed quantities, readable output). Restricted
generic CRUD tools sit underneath for API resources that are not covered by the
designed tools and do not expose raw secrets/admin data.

## Build

```sh
go build -o tandoor-mcp .
# or: go install github.com/khr4/tandoor-mcp@latest
```

Requires Go 1.26+.

## Configuration

Configured entirely via environment variables:

| Variable | Required | Description |
|---|---|---|
| `TANDOOR_URL` | yes | Instance root, e.g. `https://recipes.example.com` (no `/api` suffix). Public cleartext `http://` URLs are refused; loopback/private HTTP is allowed for local development and logs a warning. |
| `TANDOOR_TOKEN` | yes | API token — Tandoor: *Settings → API → generate*. (`TANDOOR_API_TOKEN` also accepted.) |
| `TANDOOR_INSECURE_SKIP_VERIFY` | no | `true` to skip TLS verification (self-signed instances). Logs a warning. |
| `TANDOOR_TIMEOUT` | no | Per-request timeout in seconds (default `30`). |
| `TANDOOR_OPERATION_TIMEOUT` | no | Total budget for one MCP tool call in seconds (default `120`). |
| `TANDOOR_MAX_CONCURRENCY` | no | Maximum concurrent upstream Tandoor requests (default `8`, allowed `1..64`). |
| `TANDOOR_RETRY_MAX` | no | Extra attempts for safe read requests only (default `2`, set `0` to disable). |
| `TANDOOR_RETRY_BASE_MS` | no | Base retry backoff in milliseconds (default `200`). |
| `TANDOOR_BREAKER_FAILURES` | no | Consecutive temporary upstream failures before the circuit opens (default `5`). |
| `TANDOOR_BREAKER_COOLDOWN_SECONDS` | no | Open-circuit cooldown before one half-open probe (default `10`). |
| `TANDOOR_IMAGE_DIR` | no | Directory `set_recipe_image` may read local files from. Unset disables local-file uploads (use `image_url`). |

The token is sent as `Authorization: Bearer <token>`.

Safe read requests (`GET`) retry temporary upstream failures with bounded
exponential backoff and `Retry-After` support. Mutating requests are never
blindly retried: if a timeout or temporary upstream failure leaves the commit
status unknown, the tool returns a structured `outcome_unknown` MCP error.
Failures before a request is handed to Tandoor return `not_attempted`, which is
safe to retry after the local cause is resolved. Tool errors are object-shaped
structured JSON with bounded body/cause excerpts to keep agent context useful and
small; this is an agent-safety guard, not a privacy redaction boundary.

### Transport

By default the server speaks MCP over **stdio** — the right choice for a local,
per-client launch (`.mcp.json` / `claude_desktop_config.json`). Set
`TANDOOR_HTTP_ADDR` to serve over the network instead:

| Variable | Required | Description |
|---|---|---|
| `TANDOOR_HTTP_ADDR` | no | Listen address (e.g. `:8080`, `127.0.0.1:8080`). When set, serves HTTP instead of stdio. |
| `TANDOOR_MCP_TOKEN` | when non-loopback | Static bearer token clients must present (`Authorization: Bearer …`). **Required** to bind a non-loopback address — an open endpoint grants full Tandoor access. |
| `TANDOOR_TLS_CERT` / `TANDOOR_TLS_KEY` | no | PEM cert + key. Both set → HTTPS (HTTP/2 via ALPN). Neither → cleartext with HTTP/2 cleartext (h2c). |
| `TANDOOR_HTTP_ALLOW_CLEAR` | no | Set `true` only when a non-loopback cleartext bind is protected by another encrypted transport. Without this, non-loopback cleartext is refused. |

The HTTP transport exposes the modern **Streamable HTTP** transport at `/mcp`
(request/response plus SSE streaming), the legacy **SSE** transport at `/sse`,
an unauthenticated `/healthz` liveness probe, and `/readyz`, which checks Tandoor
with the configured API token. Readiness results are briefly cached and failure
responses are sanitized so upstream error bodies are not exposed publicly.
HTTP/1.1 and HTTP/2 are both served (h2 over TLS, h2c in cleartext).

**Behind a reverse proxy** (the common case): bind loopback and let the proxy on
the same host terminate TLS. The bearer token is the trust boundary, so the SDK's
DNS-rebinding protection (which would otherwise 403 a forwarded public `Host`) is
disabled automatically when a token is set; for a token-less loopback dev server
it stays on. A non-loopback bind requires a token of at least 24 characters.
Non-loopback cleartext is refused unless `TANDOOR_HTTP_ALLOW_CLEAR=true`; prefer
a loopback bind behind a same-host TLS proxy, or set `TANDOOR_TLS_CERT/KEY`.

```sh
# behind a same-host reverse proxy (TLS terminated at the edge)
MCP_TOKEN="$(openssl rand -hex 32)"
export TANDOOR_MCP_TOKEN="$MCP_TOKEN"
TANDOOR_URL=https://recipes.example.com TANDOOR_TOKEN=xxxx \
TANDOOR_HTTP_ADDR=127.0.0.1:8080 ./tandoor-mcp
```

## Run

```sh
TANDOOR_URL=https://recipes.example.com TANDOOR_TOKEN=xxxx ./tandoor-mcp
```

Register it with an MCP client. Example (`claude_desktop_config.json` /
`.mcp.json`):

```json
{
  "mcpServers": {
    "tandoor": {
      "command": "/path/to/tandoor-mcp",
      "env": {
        "TANDOOR_URL": "https://recipes.example.com",
        "TANDOOR_TOKEN": "xxxx"
      }
    }
  }
}
```

## Docker

A static binary on `scratch` — the image is ~10 MB. Build locally:

```sh
docker build -t tandoor-mcp .
```

Or pull a published release (built and pushed to GHCR on each tag by
`.github/workflows/docker.yml`):

```sh
docker pull ghcr.io/khr4/tandoor-mcp:latest
```

Most useful with the **HTTP transport** (an stdio server in a container needs
`-i` and a client that execs into it):

```sh
docker run --rm -p 8080:8080 \
  -e TANDOOR_URL=https://recipes.example.com -e TANDOOR_TOKEN=xxxx \
  -e TANDOOR_HTTP_ADDR=:8080 -e TANDOOR_MCP_TOKEN="$(openssl rand -hex 32)" \
  ghcr.io/khr4/tandoor-mcp:latest
```

The container runs as a non-root user and ships only the binary plus CA roots
(no shell). To read local files for `set_recipe_image`, mount a directory and set
`TANDOOR_IMAGE_DIR`.

## Tools

### Designed tools (use these)

Recipes — every recipe argument accepts a **name or id**:

| Tool | Purpose |
|---|---|
| `find_recipes` | Search by words, keyword/ingredient **names** (match ALL), book, rating, or makeable-now. Requested name filters fail closed if they cannot be resolved. Returns compact cards. |
| `get_recipe` | One recipe as structured fields, an editable stored `steps[]` array, stored nutrition/properties (when set), an `edit_revision` for stale-write protection, and a Markdown view; optional `servings` re-scales the Markdown amounts only (not nutrition), leaving structured steps safe to edit. |
| `create_recipe` | Create a recipe. Ingredients as natural lines (`"2 cups flour"`, parsed into amount+unit+food) or explicit `{amount, unit, food}`; top-level `ingredients` for simple recipes or `steps[]` for multi-step. Foods/units/keywords created by name. |
| `import_recipe_from_url` | Scrape a public web page and save it. `save=false` returns a preview only when Tandoor returns parsed recipe data without saving server-side. If parsed ingredients would be dropped, saving is refused unless `allow_partial=true`; partial saves return `imported_partial` with dropped ingredients listed. |
| `update_recipe` | Targeted edits: name, description, servings, times, add/remove keywords. Keyword edits require `expected_revision` from `get_recipe`. |
| `set_recipe_steps` | Replace a recipe's steps/ingredients with a non-empty list — read `get_recipe`'s `steps[]` and `edit_revision`, edit, pass back with `expected_revision`. This tool does not clear all steps. |
| `delete_recipe` | Delete a recipe. |
| `set_recipe_image` | Set an image from exactly one source: a public remote URL, a regular local file within `TANDOOR_IMAGE_DIR`, or inline generated image bytes via `image_base64`. `image_url` is fetched and processed by Tandoor server-side, not by tandoor-mcp; upstream 5xx/timeout errors can mean Tandoor received non-image/unsupported/truncated bytes, hit an image-processing bug, or cannot reach the URL from its pod/server network. Use `image_base64`/`image_path` to upload bytes through tandoor-mcp. `image_base64` accepts raw base64 plus `image_mime_type`, or a `data:image/...;base64,...` URI. PNG, JPEG and WebP are allowed up to 8 MiB decoded. URL credentials, localhost, private, link-local and internal hosts are rejected. |
| `find_related_recipes` | Recipes sharing keywords/foods; results are returned under `recipes`. |
| `log_cooked` | Record a cook + rating (how recipes get rated). |
| `add_recipe_to_book` / `remove_recipe_from_book` / `list_recipe_books` | Organize recipes into books (book created on first add). If a named book cannot be resolved, use `list_recipe_books` before retrying. Filter by book with `find_recipes`. |

Meal planning, shopping, pantry, taxonomy:

| Tool | Purpose |
|---|---|
| `plan_meal` / `get_meal_plan` / `remove_meal_plan_entry` | Manage the meal-plan calendar (meal type by name, recipe by name/id). `get_meal_plan` returns entries under `entries`. |
| `get_shopping_list` | Current entries as readable lines plus structured amount/unit/food fields and truncation metadata. |
| `add_to_shopping_list` | Add an ad-hoc food. Pass amount/unit for a quantity; omit amount for a no-amount item. |
| `add_recipe_to_shopping` | Add a recipe's ingredients (optionally scaled). |
| `update_shopping_item` / `clear_shopping_list` | Check off / edit amount / clear entries. Clear refuses to run if the shopping list scan is truncated and returns an MCP error result with per-entry failures if any delete in the batch fails. |
| `check_shopping_items` | Check or uncheck many entries at once (incl. uncheck-all). |
| `get_pantry` / `set_food_on_hand` | Read / set foods marked on-hand. Pass exactly one of `food` or `foods[]`. Marking on-hand creates a food by name if missing; clearing requires an existing food and refuses typos; partial batch failures are MCP error results with per-food details. `get_pantry` includes truncation metadata. |
| `list_taxonomy` | List keywords, foods or units (`kind`) with ids; results are returned under `items`. |
| `merge_taxonomy` / `move_taxonomy` | Merge or re-parent a keyword/food/unit (by name or id). |

Ingredient quantities are always kept explicit: amounts and units are split out
(via Tandoor's parser for natural lines) rather than flattened into free text.

For any mutation returning `outcome_unknown` or `partial_outcome_unknown`, re-read
the affected recipe, shopping list, pantry, taxonomy or book state before
retrying. The upstream request reached Tandoor and may have committed.

For generated images, pass the final base64 bytes directly to `set_recipe_image`
as `image_base64`. This matches OpenAI Image API `data[0].b64_json`, OpenAI
Responses API `image_generation_call.result`, and MCP `ImageContent.data`; pass
the associated MIME type as `image_mime_type` unless the value is already a
`data:image/...;base64,...` URI.

For URL images, tandoor-mcp only validates and forwards `image_url`; Tandoor does
the remote download and image processing. If Tandoor returns a 500/timeout while
setting a public URL image, inspect the upstream body first. A traceback around
`handle_image` or saving a `None` object means Tandoor likely fetched something
that was not processable image bytes; otherwise check reachability from the
Tandoor runtime: egress/network policy, DNS, TLS/proxy configuration and
remote-site blocking. The error result includes the URL host, upstream body
excerpt and this hint; use `image_base64` or `image_path` when Tandoor cannot
handle external image URLs.

### Generic API tools (escape hatch)

Underneath, these reach the non-restricted resources listed by
`tandoor_resources` and custom endpoints that pass path validation, for cases the
designed tools don't cover:

`tandoor_resources`, `tandoor_list`, `tandoor_get`, `tandoor_create`,
`tandoor_update`, `tandoor_delete`, `tandoor_action`.

Secret/admin/raw-log resources such as access tokens, storage connectors, users,
spaces, uploaded user files and logs are intentionally hidden from the generic
tools because their responses are returned to the model. Custom action paths are
canonicalized and cannot use empty, `.` or `..` segments.

Some capabilities are intentionally not exposed as designed tools until their
safe contract is verified. Use the generic tools only when the designed surface
does not cover a workflow and the target resource is visible in
`tandoor_resources`. Generic API JSON responses are returned under `data`; empty
and non-JSON upstream responses use explicit `empty_response` or
`non_json_response` status objects.

## Development

```sh
make test   # httptest-backed; no network or live instance required
make vet
make lint
make install-hooks  # one-time local git-secrets hook setup
make secret-scan
make verify         # vet + test + lint + secret-scan
```

See [CLAUDE.md](CLAUDE.md) for architecture and contribution discipline.

## License

[MIT](LICENSE).
