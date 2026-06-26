# tandoor-mcp

An MCP server for [Tandoor Recipes](https://tandoor.dev). It runs over stdio and
exposes **task-oriented tools designed for agents** — find/read/create recipes,
plan meals, manage the shopping list and pantry — that hide Tandoor's nested REST
shapes (names instead of ids, parsed quantities, readable output). Generic CRUD
tools sit underneath for full coverage of every API resource.

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
| `TANDOOR_URL` | yes | Instance root, e.g. `https://recipes.example.com` (no `/api` suffix). |
| `TANDOOR_TOKEN` | yes | API token — Tandoor: *Settings → API → generate*. (`TANDOOR_API_TOKEN` also accepted.) |
| `TANDOOR_INSECURE_SKIP_VERIFY` | no | `true` to skip TLS verification (self-signed instances). Logs a warning. |
| `TANDOOR_TIMEOUT` | no | Per-request timeout in seconds (default `30`). |
| `TANDOOR_IMAGE_DIR` | no | Directory `set_recipe_image` may read local files from. Unset disables local-file uploads (use `image_url`). |

The token is sent as `Authorization: Bearer <token>`.

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

## Tools

### Designed tools (use these)

Recipes — every recipe argument accepts a **name or id**:

| Tool | Purpose |
|---|---|
| `find_recipes` | Search by words, keyword/ingredient **names** (match ALL), book, rating, or makeable-now. Returns compact cards. |
| `get_recipe` | One recipe as structured fields, an editable `steps[]` array, and a Markdown view; optional `servings` re-scales amounts. |
| `create_recipe` | Create a recipe. Ingredients as natural lines (`"2 cups flour"`, parsed into amount+unit+food) or explicit `{amount, unit, food}`; top-level `ingredients` for simple recipes or `steps[]` for multi-step. Foods/units/keywords created by name. |
| `import_recipe_from_url` | Scrape a web page and save it (`save=false` for a preview). |
| `update_recipe` | Targeted edits: name, description, servings, times, add/remove keywords. |
| `set_recipe_steps` | Replace a recipe's steps/ingredients — read `get_recipe`'s `steps[]`, edit, pass back. |
| `delete_recipe` | Delete a recipe. |
| `set_recipe_image` | Set an image from a remote URL, or a local file within `TANDOOR_IMAGE_DIR`. |
| `find_related_recipes` | Recipes sharing keywords/foods. |
| `log_cooked` | Record a cook + rating (how recipes get rated). |

Meal planning, shopping, pantry, taxonomy:

| Tool | Purpose |
|---|---|
| `plan_meal` / `get_meal_plan` / `remove_meal_plan_entry` | Manage the meal-plan calendar (meal type by name, recipe by name/id). |
| `get_shopping_list` | Current entries as readable lines. |
| `add_to_shopping_list` | Add an ad-hoc food with amount + unit. |
| `add_recipe_to_shopping` | Add a recipe's ingredients (optionally scaled). |
| `update_shopping_item` / `clear_shopping_list` | Check off / edit / clear entries. |
| `get_pantry` / `set_food_on_hand` | Read / set foods marked on-hand. |
| `list_taxonomy` | List keywords, foods or units (`kind`) with ids. |
| `merge_taxonomy` / `move_taxonomy` | Merge or re-parent a keyword/food/unit (by name or id). |

Ingredient quantities are always kept explicit: amounts and units are split out
(via Tandoor's parser for natural lines) rather than flattened into free text.

### Generic API tools (escape hatch)

Underneath, these reach **every** resource in `tandoor_resources` and any custom
endpoint, for cases the designed tools don't cover:

`tandoor_resources`, `tandoor_list`, `tandoor_get`, `tandoor_create`,
`tandoor_update`, `tandoor_delete`, `tandoor_action`.

## Development

```sh
make test   # httptest-backed; no network or live instance required
make vet
```

See [CLAUDE.md](CLAUDE.md) for architecture and contribution discipline.
