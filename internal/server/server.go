// Package server exposes the Tandoor Recipes REST API as MCP tools.
//
// Coverage is twofold: generic CRUD tools (tandoor_list/get/create/update/delete)
// plus tandoor_action reach every router-registered collection and custom
// endpoint, while a set of task-focused tools (recipe_search, recipe_from_source,
// merge/move, ...) give the common workflows precise, well-documented inputs.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is reported to MCP clients during initialization.
const Version = "0.1.0"

// handlers binds tool implementations to a Tandoor client.
type handlers struct {
	c *tandoor.Client
}

// New builds an MCP server with every Tandoor tool registered.
func New(c *tandoor.Client) *mcp.Server {
	h := &handlers{c: c}
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tandoor-mcp",
		Title:   "Tandoor Recipes",
		Version: Version,
	}, nil)
	h.register(s)
	return s
}

// register wires up all tools. Out is always `any`, which makes the SDK skip
// output-schema inference: handlers return raw Tandoor JSON (objects or arrays)
// as text content without per-tool result schemas.
func (h *handlers) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_resources",
		Description: "List every Tandoor resource the CRUD tools can operate on, with its path and a short description. Call this first to discover available resource names.",
	}, h.Resources)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_list",
		Description: "List/search a resource collection. Returns a paginated envelope {count,next,previous,results}. Use `query` for full-text search, `filters` for any resource-specific query parameters.",
	}, h.List)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_get",
		Description: "Fetch a single object of a resource by id.",
	}, h.Get)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_create",
		Description: "Create an object in a resource collection. `data` holds the object fields (see the Tandoor API for each resource's shape).",
	}, h.Create)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_update",
		Description: "Update an object. Defaults to a partial update (PATCH); set `full` to replace the whole object (PUT).",
	}, h.Update)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_delete",
		Description: "Delete an object of a resource by id.",
	}, h.Delete)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tandoor_action",
		Description: "Escape hatch for any endpoint not covered by a dedicated tool (custom viewset actions, function views). Call an arbitrary API path with a method, query string and JSON body. Path is relative to /api/, e.g. 'meal-plan/ical/', 'fdc-search/', 'switch-active-space/2/', 'sync_all/'.",
	}, h.Action)

	// --- Designed, agent-ergonomic tools layered on top of the generic API ---

	// Recipes.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_recipes",
		Description: "Search recipes by words, keyword names, ingredient/food names, recipe book, minimum rating, or what you can make from on-hand foods. Returns compact recipe cards (id, name, rating, times, tags).",
	}, h.FindRecipes)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_recipe",
		Description: "Get one recipe rendered as readable Markdown: metadata, ingredient lines with quantities, and numbered steps.",
	}, h.GetRecipe)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_recipe",
		Description: "Create a recipe. Ingredients may be natural lines (\"2 cups flour\") which are parsed into amount+unit+food, or explicit {amount, unit, food}. Foods, units and keywords are created by name as needed.",
	}, h.CreateRecipe)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "import_recipe_from_url",
		Description: "Import a recipe from a web page: scrapes the page and saves it (set save=false for a preview only).",
	}, h.ImportRecipeFromURL)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_recipe",
		Description: "Apply targeted edits to a recipe (name, description, servings, times, source, add/remove keywords) without resending the whole recipe.",
	}, h.UpdateRecipe)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_recipe_image",
		Description: "Set a recipe's image from a local file path or a remote image URL.",
	}, h.SetRecipeImage)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_related_recipes",
		Description: "List recipes related to a recipe (sharing keywords/foods).",
	}, h.FindRelatedRecipes)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "log_cooked",
		Description: "Record that a recipe was cooked, optionally with a rating (0-5), servings and a comment. This is how recipe ratings are set.",
	}, h.LogCooked)

	// Meal planning.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan_meal",
		Description: "Add an entry to the meal-plan calendar for a date and meal type (by name), optionally with a recipe and servings.",
	}, h.PlanMeal)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_meal_plan",
		Description: "List meal-plan entries, optionally within a date range.",
	}, h.GetMealPlan)

	// Shopping list.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_shopping_list",
		Description: "List shopping list entries as readable lines (unchecked by default).",
	}, h.GetShoppingList)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_to_shopping_list",
		Description: "Add an ad-hoc food to the shopping list with an amount and optional unit.",
	}, h.AddToShoppingList)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_recipe_to_shopping",
		Description: "Add all of a recipe's ingredients to the shopping list, optionally scaling servings.",
	}, h.AddRecipeToShopping)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_shopping_item",
		Description: "Check off (or edit the amount of) a single shopping list entry.",
	}, h.UpdateShoppingItem)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "clear_shopping_list",
		Description: "Remove shopping list entries (checked-off items by default, or all).",
	}, h.ClearShoppingList)

	// Pantry / on-hand.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pantry",
		Description: "List foods currently marked on-hand (in the pantry).",
	}, h.GetPantry)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_food_on_hand",
		Description: "Mark a food as on-hand (in the pantry) or clear it. Used by makeable_now searches.",
	}, h.SetFoodOnHand)

	// Taxonomy discovery + maintenance.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_keywords",
		Description: "List keyword tags (id + name), optionally filtered by name.",
	}, h.ListKeywords)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_foods",
		Description: "List foods (id + name), optionally filtered by name.",
	}, h.ListFoods)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_units",
		Description: "List measurement units (id + name), optionally filtered by name.",
	}, h.ListUnits)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "keyword_merge",
		Description: "Merge one keyword into another: recipes are re-tagged to the target and the source keyword is deleted.",
	}, h.KeywordMerge)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "keyword_move",
		Description: "Move a keyword to a new parent in the keyword tree (parent 0 = top level).",
	}, h.KeywordMove)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "food_merge",
		Description: "Merge one food into another: ingredients are re-pointed to the target and the source food is deleted.",
	}, h.FoodMerge)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "food_move",
		Description: "Move a food to a new parent in the food tree (parent 0 = top level).",
	}, h.FoodMove)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "unit_merge",
		Description: "Merge one unit into another: ingredients are re-pointed to the target and the source unit is deleted.",
	}, h.UnitMerge)
}

// --- result helpers ---

// rawResult returns Tandoor's JSON response as pretty-printed text content.
func rawResult(raw json.RawMessage) (*mcp.CallToolResult, any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return textResult("(empty response)")
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return textResult(string(raw)) // not JSON; pass through verbatim
	}
	return textResult(buf.String())
}

// jsonResult marshals a Go value to pretty JSON text content.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding result: %w", err)
	}
	return textResult(string(b))
}

func textResult(text string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}
