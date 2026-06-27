// Package server exposes Tandoor Recipes as MCP tools in two layers: designed,
// task-oriented tools (find_recipes, create_recipe, import_recipe_from_url,
// plan_meal, shopping, pantry, ...) that take names and parsed quantities and
// return compact results, and generic tools (tandoor_list/get/create/update/
// delete/action + tandoor_resources) that reach every other API resource.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultOperationTimeout = 120 * time.Second

// Version is reported to MCP clients during initialization. Release builds can
// override it with -ldflags "-X github.com/khr4/tandoor-mcp/internal/server.Version=vX.Y.Z".
var Version = "dev"

func reportedVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
		var rev string
		modified := false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 12 {
					rev = s.Value[:12]
				} else {
					rev = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if rev != "" {
			if modified {
				return "dev-" + rev + "-dirty"
			}
			return "dev-" + rev
		}
	}
	return "dev"
}

// Options configures server-layer policy that isn't part of the API client.
type Options struct {
	// ImageDir, when non-empty, is the only directory set_recipe_image may read
	// local files from. Empty disables local-file image uploads (image_url only).
	ImageDir string
	// OperationTimeout bounds one MCP tool call across all upstream requests.
	// Zero uses the default.
	OperationTimeout time.Duration
}

// handlers binds tool implementations to a Tandoor client.
type handlers struct {
	c                *tandoor.Client
	imageDir         string
	operationTimeout time.Duration
}

// New builds an MCP server with every Tandoor tool registered.
func New(c *tandoor.Client, opts Options) *mcp.Server {
	if opts.OperationTimeout == 0 {
		opts.OperationTimeout = defaultOperationTimeout
	}
	h := &handlers{c: c, imageDir: opts.ImageDir, operationTimeout: opts.OperationTimeout}
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tandoor-mcp",
		Title:   "Tandoor Recipes",
		Version: reportedVersion(),
	}, nil)
	h.register(s)
	return s
}

const toolErrorExcerptRunes = 1200

// register wires up all tools. Out is always `any`, so the SDK skips output-schema
// inference and handlers return JSON/text content with no per-tool result schema.
func (h *handlers) register(s *mcp.Server) {
	// Recipes.
	addTool(s, h, &mcp.Tool{Name: "find_recipes", Description: "Search recipes by words, keyword/ingredient names (match ALL), recipe book, minimum rating, or what you can make from on-hand foods. Returns compact recipe cards (id, name, rating, times, tags)."}, h.findRecipes)
	addTool(s, h, &mcp.Tool{Name: "get_recipe", Description: "Get one recipe (by name or id) as structured fields, a structured steps[] array (editable and accepted directly by set_recipe_steps), stored nutrition/properties when present, an edit_revision for safe replacement edits, and a readable Markdown view. Optionally re-scale ingredient amounts to a serving count (nutrition is shown as stored, not re-scaled)."}, h.getRecipe)
	addTool(s, h, &mcp.Tool{Name: "create_recipe", Description: "Create a recipe. Provide each ingredient as EITHER a natural line (\"2 cups flour\", parsed into amount+unit+food) OR structured {amount, unit, food} — not both. Omit amount for no-quantity items (\"salt to taste\"). For a section header use {is_header:true, note:\"For the sauce\"}. Use top-level ingredients for a simple recipe, or steps[] for multi-step (not both). Foods/units/keywords are created by name. If result status is outcome_unknown, re-read state/candidate ids before retrying; the write may have committed."}, h.createRecipe)
	addTool(s, h, &mcp.Tool{Name: "import_recipe_from_url", Description: "Import a recipe from a public http/https web page; URLs with credentials, localhost, private, link-local, or internal hosts are rejected. Scrapes and saves it (save=false returns a parsed preview). If result status is outcome_unknown, re-read state/candidate ids before retrying; the write may have committed."}, h.importRecipeFromURL)
	addTool(s, h, &mcp.Tool{Name: "update_recipe", Description: "Edit a recipe (by name or id): name, description, servings, times, source, add/remove keywords. Keyword edits require expected_revision from get_recipe. Successful writes return a fresh edit_revision for the next guarded edit. If result status is outcome_unknown, re-read the recipe before retrying; the write may have committed."}, h.updateRecipe)
	addTool(s, h, &mcp.Tool{Name: "set_recipe_steps", Description: "Replace a recipe's steps and ingredients with a non-empty steps list. Requires expected_revision from get_recipe. To edit, read get_recipe's steps[], change what you need, and pass them back here (same shape). Successful writes return a fresh edit_revision for the next guarded edit. If result status is outcome_unknown, re-read the recipe before retrying; the write may have committed."}, h.setRecipeSteps)
	addTool(s, h, &mcp.Tool{Name: "delete_recipe", Description: "Delete a recipe (by name or id). If result status is outcome_unknown, re-read state before retrying; the delete may have committed."}, h.deleteRecipe)
	addTool(s, h, &mcp.Tool{Name: "set_recipe_image", Description: "Set a recipe's image from exactly one source: image_url for a public http/https URL, image_path for a regular local file within the configured image directory, or image_base64 for inline generated image bytes. image_url is fetched and processed by Tandoor server-side, so 5xx/timeout failures can mean Tandoor received non-image/unsupported/truncated bytes, hit an image-processing bug, or cannot reach the URL from its pod/server network; use image_base64/image_path to upload bytes through tandoor-mcp. image_base64 accepts raw base64 plus image_mime_type, or a data:image/...;base64 URI; PNG, JPEG and WebP are allowed up to 8 MiB decoded. URL credentials, localhost, private, link-local, and internal hosts are rejected. If result status is outcome_unknown, re-read the recipe before retrying; the write may have committed."}, h.setRecipeImage)
	addTool(s, h, &mcp.Tool{Name: "find_related_recipes", Description: "List recipes related to a recipe (sharing keywords/foods)."}, h.findRelatedRecipes)
	addTool(s, h, &mcp.Tool{Name: "log_cooked", Description: "Record that a recipe was cooked, optionally with a rating (0-5), servings and comment. This is how recipe ratings are set. If result status is outcome_unknown, re-read cook logs/recipe state before retrying; the write may have committed."}, h.logCooked)
	addTool(s, h, &mcp.Tool{Name: "get_cook_log", Description: "List cook history, optionally filtered by recipe/date/rating. Returns compact log entries and a summary so agents can review ratings and recent cooking without raw cook-log payloads."}, h.getCookLog)

	// Recipe books (organizing recipes into collections).
	addTool(s, h, &mcp.Tool{Name: "add_recipe_to_book", Description: "Add a recipe (by name or id) to a recipe book (by name); the book is created if it does not exist. Idempotent. If result status is outcome_unknown, use list_recipe_books before retrying; the write may have committed."}, h.addRecipeToBook)
	addTool(s, h, &mcp.Tool{Name: "remove_recipe_from_book", Description: "Remove a recipe (by name or id) from a recipe book (by name or id). Reports not_in_book if it wasn't a member. If result status is outcome_unknown, use list_recipe_books before retrying; the delete may have committed."}, h.removeRecipeFromBook)
	addTool(s, h, &mcp.Tool{Name: "list_recipe_books", Description: "List recipe books (id, name, description). Pass a recipe (name or id) to list only the books that recipe is in. Filter recipes by book with find_recipes' book argument."}, h.listRecipeBooks)

	// Meal planning.
	addTool(s, h, &mcp.Tool{Name: "plan_meal", Description: "Add an entry to the meal-plan calendar for a date and meal type (by name), optionally with a recipe (by name or id) and servings. If result status is outcome_unknown, re-read the meal plan before retrying; the write may have committed."}, h.planMeal)
	addTool(s, h, &mcp.Tool{Name: "get_meal_plan", Description: "List meal-plan entries, optionally within a date range (YYYY-MM-DD)."}, h.getMealPlan)
	addTool(s, h, &mcp.Tool{Name: "update_meal_plan_entry", Description: "Update a meal-plan entry by id (from get_meal_plan): date, meal type, recipe, servings, title or note. If result status is outcome_unknown, re-read the meal plan before retrying; the write may have committed."}, h.updateMealPlanEntry)
	addTool(s, h, &mcp.Tool{Name: "remove_meal_plan_entry", Description: "Remove a meal-plan entry by id (from get_meal_plan)."}, h.removeMealPlanEntry)

	// Shopping list.
	addTool(s, h, &mcp.Tool{Name: "get_shopping_list", Description: "List shopping list entries as readable lines (unchecked by default)."}, h.getShoppingList)
	addTool(s, h, &mcp.Tool{Name: "add_to_shopping_list", Description: "Add an ad-hoc food to the shopping list. Pass amount/unit for a quantity; omit amount for a no-amount item such as olive oil. If result status is outcome_unknown, re-read the shopping list before retrying; the write may have committed."}, h.addToShoppingList)
	addTool(s, h, &mcp.Tool{Name: "add_recipe_to_shopping", Description: "Add all of a recipe's ingredients to the shopping list (recipe by name or id), optionally scaling servings. If result status is outcome_unknown, re-read the shopping list before retrying; the write may have committed."}, h.addRecipeToShopping)
	addTool(s, h, &mcp.Tool{Name: "update_shopping_item", Description: "Check off or change the amount of a single shopping list entry. Provide checked and/or amount; food and unit cannot be edited here."}, h.updateShoppingItem)
	addTool(s, h, &mcp.Tool{Name: "clear_shopping_list", Description: "Remove shopping list entries (checked-off items by default, or all). Partial failures are MCP error results with per-entry details; if status is partial_outcome_unknown, re-read the shopping list before retrying."}, h.clearShoppingList)
	addTool(s, h, &mcp.Tool{Name: "check_shopping_items", Description: "Check or uncheck many shopping list entries at once (ids from get_shopping_list). Pass checked=false to un-check; to un-check everything, get_shopping_list with include_checked=true and pass all ids. If result status is outcome_unknown, re-read the shopping list before retrying; the write may have committed."}, h.checkShoppingItems)

	// Pantry / on-hand.
	addTool(s, h, &mcp.Tool{Name: "get_pantry", Description: "List foods currently marked on-hand (in the pantry). Scans up to the configured page cap and returns truncated=true if more foods may exist."}, h.getPantry)
	addTool(s, h, &mcp.Tool{Name: "set_food_on_hand", Description: "Mark foods as on-hand (in the pantry) or clear them. Pass exactly one of food for one item or foods[] for several. Used by makeable_now searches. Partial failures are MCP error results with per-food details; if status is partial_outcome_unknown, re-read pantry/food state before retrying."}, h.setFoodOnHand)
	addTool(s, h, &mcp.Tool{Name: "get_inventory", Description: "List inventory entries with food, amount, unit, location, expiry and note fields. Filter by food or inventory location name/id; include_empty=true includes zero-amount entries."}, h.getInventory)

	// Taxonomy (keywords / foods / units).
	addTool(s, h, &mcp.Tool{Name: "list_taxonomy", Description: "List keywords, foods or units (id + name) for a given kind, optionally filtered by name. Singular kinds keyword/food/unit and plural aliases are accepted."}, h.listTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "create_taxonomy", Description: "Create a keyword, food or unit by name. Foods and units may include plural_name; keywords and foods may be moved under a parent after creation."}, h.createTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "rename_taxonomy", Description: "Rename or update description/plural_name for a keyword, food or unit by name or id."}, h.renameTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "merge_taxonomy", Description: "Merge one keyword/food/unit into another (by name or id); the source is removed. If result status is outcome_unknown, re-read taxonomy state before retrying; the merge may have committed."}, h.mergeTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "move_taxonomy", Description: "Re-parent a keyword or food in its tree (by name or id); omit parent for top level. If result status is outcome_unknown, re-read taxonomy state before retrying; the move may have committed."}, h.moveTaxonomy)

	// Generic API tools (escape hatch over every resource in tandoor_resources).
	addTool(s, h, &mcp.Tool{Name: "tandoor_resources", Description: "List non-restricted Tandoor resources available to the generic tools, including preferred designed tools when one exists. Prefer designed tools first."}, h.resourceCatalog)
	addTool(s, h, &mcp.Tool{Name: "tandoor_list", Description: "Escape hatch: list/search a non-restricted resource collection after designed tools do not cover the workflow. Raw Tandoor responses are returned under data."}, h.genericList)
	addTool(s, h, &mcp.Tool{Name: "tandoor_get", Description: "Escape hatch: fetch one object from a non-restricted resource after designed tools do not cover the workflow. Raw Tandoor responses are returned under data."}, h.genericGet)
	addTool(s, h, &mcp.Tool{Name: "tandoor_create", Description: "Escape hatch: create an object in an audited generic mutation resource from raw fields. Generic mutations are blocked for recipes, steps, shopping entries, inventory entries, imports, logs, admin/user/file/AI/storage/sync surfaces; prefer designed tools. If result status is outcome_unknown, re-read state before retrying; the write may have committed."}, h.genericCreate)
	addTool(s, h, &mcp.Tool{Name: "tandoor_update", Description: "Escape hatch: update an audited generic mutation resource (PATCH, or PUT with full=true). Generic mutations are blocked for recipes, steps, shopping entries, inventory entries, imports, logs, admin/user/file/AI/storage/sync surfaces; prefer designed tools. If result status is outcome_unknown, re-read state before retrying; the write may have committed."}, h.genericUpdate)
	addTool(s, h, &mcp.Tool{Name: "tandoor_delete", Description: "Escape hatch: delete an object from an audited generic mutation resource. Generic mutations are blocked for recipes, steps, shopping entries, inventory entries, imports, logs, admin/user/file/AI/storage/sync surfaces; prefer designed tools. If result status is outcome_unknown, re-read state before retrying; the delete may have committed."}, h.genericDelete)
	addTool(s, h, &mcp.Tool{Name: "tandoor_action", Description: "Escape hatch: call only audited custom /api/ endpoints: recipe/<id>/related/, recipe/flat/, ingredient-parser/post/, fdc-search/, and server-settings/current/. File/download/share/sync/AI/space-switch/admin-like paths are denied. Raw responses are returned under data. If result status is outcome_unknown, re-read state before retrying; the write may have committed."}, h.genericAction)
}

func addTool[I any](s *mcp.Server, h *handlers, tool *mcp.Tool, fn func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error)) {
	mcp.AddTool(s, tool, withOperationBudget(h, fn))
}

func withOperationBudget[I any](h *handlers, fn func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in I) (*mcp.CallToolResult, any, error) {
		if h.operationTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, h.operationTimeout)
			defer cancel()
		}
		res, out, err := fn(ctx, req, in)
		if err != nil {
			return toolErrorResult(err)
		}
		return res, out, err
	}
}

// --- result helpers ---

// rawResult turns raw Tandoor responses into object-shaped tool results.
func rawResult(raw json.RawMessage) (*mcp.CallToolResult, any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return jsonResult(map[string]any{"status": "empty_response"})
	}
	if len(raw) > maxGenericResultBytes {
		return jsonErrorResult(map[string]any{
			"status":                "result_too_large",
			"message":               "Tandoor response is too large for a raw MCP result; narrow the query or use pagination",
			"max_bytes":             maxGenericResultBytes,
			"received_bytes":        len(raw),
			"body_truncated":        true,
			"body_truncated_reason": "generic result size limit",
		})
	}
	var structured any
	if err := json.Unmarshal(raw, &structured); err != nil {
		excerpt, truncated := excerptString(string(raw), toolErrorExcerptRunes)
		return jsonResult(map[string]any{"status": "non_json_response", "body_excerpt": excerpt, "body_truncated": truncated})
	}
	return jsonResult(map[string]any{"data": structured})
}

// jsonResult marshals a Go value to pretty JSON text content.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	return callToolResult(false, v)
}

func jsonErrorResult(v any) (*mcp.CallToolResult, any, error) {
	return callToolResult(true, v)
}

func callToolResult(isError bool, v any) (*mcp.CallToolResult, any, error) {
	structured, err := objectStructured(v)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.MarshalIndent(structured, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding result: %w", err)
	}
	return &mcp.CallToolResult{
		IsError:           isError,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: structured,
	}, nil, nil
}

func objectStructured(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding result: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, fmt.Errorf("decoding result object: %w", err)
	}
	if obj, ok := decoded.(map[string]any); ok {
		return obj, nil
	}
	return map[string]any{"value": decoded}, nil
}

func toolErrorResult(err error) (*mcp.CallToolResult, any, error) {
	return jsonErrorResult(toolErrorObject(err))
}

func toolErrorObject(err error) map[string]any {
	var unknown *tandoor.OutcomeUnknownError
	if errors.As(err, &unknown) {
		return outcomeUnknownObject(unknown, nil)
	}
	var notAttempted *tandoor.NotAttemptedError
	if errors.As(err, &notAttempted) {
		out := map[string]any{
			"status":  "not_attempted",
			"method":  notAttempted.Method,
			"path":    notAttempted.Path,
			"message": "request was not sent to Tandoor; retry is safe after resolving the cause",
		}
		addCauseFields(out, notAttempted.Cause)
		return out
	}
	var apiErr *tandoor.APIError
	if errors.As(err, &apiErr) {
		out := map[string]any{
			"status":      "upstream_error",
			"method":      apiErr.Method,
			"path":        apiErr.Path,
			"status_code": apiErr.StatusCode,
			"status_text": http.StatusText(apiErr.StatusCode),
			"message":     fmt.Sprintf("Tandoor returned HTTP %d %s", apiErr.StatusCode, http.StatusText(apiErr.StatusCode)),
		}
		addRetryAfter(out, apiErr.RetryAfter)
		addBodyExcerpt(out, apiErr.Body)
		return out
	}
	status := "validation_error"
	message := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		status = "timeout"
		message = "operation timed out"
	} else if errors.Is(err, context.Canceled) {
		status = "canceled"
		message = "operation was canceled"
	} else if looksInternal(err) {
		status = "internal_error"
	}
	out := map[string]any{"status": status, "message": message}
	if message != err.Error() {
		excerpt, truncated := excerptString(err.Error(), toolErrorExcerptRunes)
		out["cause_excerpt"] = excerpt
		out["cause_truncated"] = truncated
	}
	return out
}

func outcomeUnknownResult(err *tandoor.OutcomeUnknownError, extra map[string]any) (*mcp.CallToolResult, any, error) {
	return jsonErrorResult(outcomeUnknownObject(err, extra))
}

func outcomeUnknownObject(err *tandoor.OutcomeUnknownError, extra map[string]any) map[string]any {
	out := map[string]any{
		"status":  "outcome_unknown",
		"method":  err.Method,
		"path":    err.Path,
		"message": "write outcome unknown; re-read state before retrying because the write may have committed",
	}
	addCauseFields(out, err.Cause)
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func addCauseFields(out map[string]any, err error) {
	if err == nil {
		return
	}
	var apiErr *tandoor.APIError
	if errors.As(err, &apiErr) {
		out["cause_status"] = "upstream_error"
		out["status_code"] = apiErr.StatusCode
		out["status_text"] = http.StatusText(apiErr.StatusCode)
		addRetryAfter(out, apiErr.RetryAfter)
		addBodyExcerpt(out, apiErr.Body)
		return
	}
	var breaker *tandoor.BreakerOpenError
	if errors.As(err, &breaker) {
		out["cause_status"] = "breaker_open"
		addRetryAfter(out, breaker.RetryAfter)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		out["cause_status"] = "timeout"
	}
	excerpt, truncated := excerptString(err.Error(), toolErrorExcerptRunes)
	out["cause_excerpt"] = excerpt
	out["cause_truncated"] = truncated
}

func addRetryAfter(out map[string]any, d time.Duration) {
	if d > 0 {
		out["retry_after_ms"] = int64(d / time.Millisecond)
	}
}

func addBodyExcerpt(out map[string]any, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	excerpt, truncated := excerptString(body, toolErrorExcerptRunes)
	out["body_excerpt"] = excerpt
	out["body_truncated"] = truncated
}

func excerptString(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	if limit <= 0 {
		return "", s != ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	return string(runes[:limit]), true
}

func looksInternal(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "decoding ") ||
		strings.Contains(msg, "encoding ") ||
		strings.Contains(msg, "response exceeds")
}

func logErrorSummary(err error) string {
	b, jsonErr := json.Marshal(toolErrorObject(err))
	if jsonErr != nil {
		return err.Error()
	}
	return string(b)
}

func failureObject(err error, context map[string]any) map[string]any {
	out := toolErrorObject(err)
	for k, v := range context {
		out[k] = v
	}
	return out
}

func hasFailureStatus(failures []map[string]any, status string) bool {
	for _, f := range failures {
		if f["status"] == status {
			return true
		}
	}
	return false
}
