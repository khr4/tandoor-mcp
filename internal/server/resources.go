package server

// Resource describes one Tandoor API collection reachable through the generic
// CRUD tools. Path is the route prefix under /api/ (the value Tandoor's router
// registers); it doubles as the canonical name callers pass as "resource".
type Resource struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Desc           string   `json:"description"`
	PreferredTools []string `json:"preferred_tools,omitempty"`
}

func resource(name, path, desc string, preferredTools ...string) Resource {
	return Resource{Name: name, Path: path, Desc: desc, PreferredTools: preferredTools}
}

// resources is the full catalog of Tandoor router-registered collections, taken
// from cookbook/urls.py. The generic tools accept any name listed here; the API
// itself enforces which verbs each collection supports (returning 405 otherwise).
var resources = []Resource{
	resource("recipe", "recipe", "Recipes. Prefer designed recipe tools for searching, reading and safe edits.", "find_recipes", "get_recipe", "create_recipe", "update_recipe", "set_recipe_steps", "delete_recipe"),
	resource("recipe-book", "recipe-book", "Recipe books (collections of recipes).", "list_recipe_books", "add_recipe_to_book", "remove_recipe_from_book"),
	resource("recipe-book-entry", "recipe-book-entry", "Membership linking a recipe to a recipe book.", "add_recipe_to_book", "remove_recipe_from_book", "list_recipe_books"),
	resource("recipe-import", "recipe-import", "Recipes discovered in synced storage, pending import."),
	resource("step", "step", "Steps belonging to a recipe."),
	resource("ingredient", "ingredient", "Ingredients (food + amount + unit) within a step."),
	resource("food", "food", "Foods (ingredients master list).", "list_taxonomy", "get_pantry", "set_food_on_hand"),
	resource("food-inherit-field", "food-inherit-field", "Fields a child food inherits from its parent."),
	resource("keyword", "keyword", "Keywords/tags applied to recipes (tree-structured).", "list_taxonomy", "merge_taxonomy", "move_taxonomy"),
	resource("unit", "unit", "Measurement units.", "list_taxonomy", "merge_taxonomy"),
	resource("unit-conversion", "unit-conversion", "Unit conversion rules, optionally food-specific."),
	resource("property", "property", "Property values (e.g. nutrition) attached to foods/recipes."),
	resource("property-type", "property-type", "Property type definitions (e.g. Calories, Fat)."),
	resource("meal-plan", "meal-plan", "Meal plan entries on the calendar.", "plan_meal", "get_meal_plan", "remove_meal_plan_entry"),
	resource("meal-type", "meal-type", "Meal types (Breakfast, Lunch, ...) used by the meal plan.", "plan_meal"),
	resource("auto-plan", "auto-plan", "Auto meal-plan generator (create-only)."),
	resource("shopping-list", "shopping-list", "Shopping lists (legacy aggregate endpoint)."),
	resource("shopping-list-entry", "shopping-list-entry", "Individual shopping list entries.", "get_shopping_list", "add_to_shopping_list", "update_shopping_item", "clear_shopping_list", "check_shopping_items"),
	resource("shopping-list-recipe", "shopping-list-recipe", "Recipe groupings within a shopping list.", "add_recipe_to_shopping", "get_shopping_list"),
	resource("supermarket", "supermarket", "Supermarkets with ordered category layouts."),
	resource("supermarket-category", "supermarket-category", "Supermarket aisle/categories."),
	resource("supermarket-category-relation", "supermarket-category-relation", "Category ordering within a supermarket."),
	resource("inventory-location", "inventory-location", "Inventory/pantry storage locations."),
	resource("inventory-entry", "inventory-entry", "On-hand inventory entries.", "get_pantry", "set_food_on_hand"),
	resource("inventory-log", "inventory-log", "Inventory change log."),
	resource("storage", "storage", "External storage backends (Dropbox, Nextcloud, local)."),
	resource("sync", "sync", "Synced storage folders watched for recipe files."),
	resource("sync-log", "sync-log", "Storage sync run log."),
	resource("connector-config", "connector-config", "Outbound connector configurations (e.g. Home Assistant)."),
	resource("automation", "automation", "Import/parse automations applied to incoming recipes."),
	resource("custom-filter", "custom-filter", "Saved search filters."),
	resource("cook-log", "cook-log", "Per-recipe cook log entries."),
	resource("view-log", "view-log", "Per-recipe view history."),
	resource("import-log", "import-log", "Bulk import run log."),
	resource("export-log", "export-log", "Bulk export run log."),
	resource("bookmarklet-import", "bookmarklet-import", "Pages captured via the bookmarklet, pending import."),
	resource("user-file", "user-file", "Uploaded user files."),
	resource("user", "user", "Users visible in the active space."),
	resource("user-preference", "user-preference", "Per-user preferences."),
	resource("user-space", "user-space", "User-to-space memberships and roles."),
	resource("space", "space", "Spaces (tenants)."),
	resource("household", "household", "Households within a space."),
	resource("group", "group", "Permission groups."),
	resource("invite-link", "invite-link", "Space invite links."),
	resource("access-token", "access-token", "API access tokens."),
	resource("search-fields", "search-fields", "Available search fields (read-only)."),
	resource("search-preference", "search-preference", "Per-user search behaviour preferences."),
	resource("ai-provider", "ai-provider", "Configured AI providers."),
	resource("ai-log", "ai-log", "AI request log."),
	resource("localization", "localization", "Localization settings (read-only)."),
	resource("server-settings", "server-settings", "Server settings (read-only)."),
	resource("ingredient-parser", "ingredient-parser", "Natural-language ingredient parser."),
}

var resourceByName = func() map[string]Resource {
	m := make(map[string]Resource, len(resources))
	for _, r := range resources {
		m[r.Name] = r
	}
	return m
}()

// restrictedResources hold credentials, administrative surfaces or raw PII-ish
// logs and must not be reachable through the generic tools, which expose raw API
// payloads under the data envelope.
var restrictedResources = map[string]bool{
	"storage":            true,
	"sync":               true,
	"sync-log":           true,
	"ai-provider":        true,
	"ai-log":             true,
	"access-token":       true,
	"connector-config":   true,
	"invite-link":        true, // carries a bearer token to join the space
	"user":               true,
	"user-preference":    true,
	"user-space":         true,
	"space":              true,
	"household":          true,
	"group":              true,
	"user-file":          true,
	"view-log":           true,
	"import-log":         true,
	"export-log":         true,
	"bookmarklet-import": true,
}

func lookupResource(name string) (Resource, bool) {
	r, ok := resourceByName[name]
	return r, ok
}
