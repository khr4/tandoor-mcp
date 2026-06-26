package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type apiShoppingEntry struct {
	ID      int     `json:"id"`
	Amount  flexNum `json:"amount"`
	Unit    *named  `json:"unit"`
	Food    *named  `json:"food"`
	Checked bool    `json:"checked"`
}

type shoppingItem struct {
	ID      int    `json:"id"`
	Item    string `json:"item"`
	Checked bool   `json:"checked"`
}

func (e apiShoppingEntry) line() string {
	var parts []string
	if a := e.Amount.String(); a != "" && a != "0" {
		parts = append(parts, a)
	}
	if e.Unit != nil && e.Unit.Name != "" {
		parts = append(parts, e.Unit.Name)
	}
	if e.Food != nil && e.Food.Name != "" {
		parts = append(parts, e.Food.Name)
	}
	return strings.Join(parts, " ")
}

// ---- get_shopping_list ----

type getShoppingInput struct {
	IncludeChecked *bool `json:"include_checked,omitempty" jsonschema:"include items already checked off (default false)"`
}

// GetShoppingList returns current shopping list entries as readable lines.
func (h *handlers) GetShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in getShoppingInput) (*mcp.CallToolResult, any, error) {
	entries, err := h.shoppingEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	includeChecked := in.IncludeChecked != nil && *in.IncludeChecked
	items := make([]shoppingItem, 0, len(entries))
	for _, e := range entries {
		if e.Checked && !includeChecked {
			continue
		}
		items = append(items, shoppingItem{ID: e.ID, Item: e.line(), Checked: e.Checked})
	}
	return jsonResult(items)
}

func (h *handlers) shoppingEntries(ctx context.Context) ([]apiShoppingEntry, error) {
	raw, err := h.c.Do(ctx, http.MethodGet, "shopping-list-entry/", nil, nil)
	if err != nil {
		return nil, err
	}
	var entries []apiShoppingEntry
	if err := decodeList(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding shopping list: %w", err)
	}
	return entries, nil
}

// ---- add_to_shopping_list ----

type addShoppingInput struct {
	Food   string   `json:"food" jsonschema:"food name to add; created if missing"`
	Amount *float64 `json:"amount,omitempty" jsonschema:"quantity (default 1)"`
	Unit   string   `json:"unit,omitempty" jsonschema:"unit name for the amount, e.g. g, cup"`
}

// AddToShoppingList adds an ad-hoc food to the shopping list.
func (h *handlers) AddToShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in addShoppingInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Food) == "" {
		return nil, nil, fmt.Errorf("food is required")
	}
	amount := 1.0
	if in.Amount != nil {
		amount = *in.Amount
	}
	body := map[string]any{
		"food":    map[string]any{"name": strings.TrimSpace(in.Food)},
		"amount":  amount,
		"checked": false,
	}
	if u := strings.TrimSpace(in.Unit); u != "" {
		body["unit"] = map[string]any{"name": u}
	}
	if _, err := h.c.Do(ctx, http.MethodPost, "shopping-list-entry/", nil, body); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("added %s to the shopping list", in.Food))
}

// ---- add_recipe_to_shopping ----

type addRecipeShoppingInput struct {
	RecipeID int  `json:"recipe_id" jsonschema:"recipe whose ingredients to add"`
	Servings *int `json:"servings,omitempty" jsonschema:"servings to scale the amounts to"`
}

// AddRecipeToShopping adds all of a recipe's ingredients to the shopping list.
func (h *handlers) AddRecipeToShopping(ctx context.Context, _ *mcp.CallToolRequest, in addRecipeShoppingInput) (*mcp.CallToolResult, any, error) {
	body := map[string]any{}
	setInt(body, "servings", in.Servings)
	if _, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("recipe/%d/shopping/", in.RecipeID), nil, body); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("added ingredients of recipe %d to the shopping list", in.RecipeID))
}

// ---- update_shopping_item ----

type updateShoppingInput struct {
	ID      int      `json:"id" jsonschema:"shopping list entry id"`
	Checked *bool    `json:"checked,omitempty" jsonschema:"mark bought (true) or not bought (false)"`
	Amount  *float64 `json:"amount,omitempty" jsonschema:"new quantity"`
}

// UpdateShoppingItem checks off or edits a single shopping list entry.
func (h *handlers) UpdateShoppingItem(ctx context.Context, _ *mcp.CallToolRequest, in updateShoppingInput) (*mcp.CallToolResult, any, error) {
	body := map[string]any{}
	if in.Checked != nil {
		body["checked"] = *in.Checked
	}
	if in.Amount != nil {
		body["amount"] = *in.Amount
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("provide checked and/or amount")
	}
	if _, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("shopping-list-entry/%d/", in.ID), nil, body); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("updated shopping list entry %d", in.ID))
}

// ---- clear_shopping_list ----

type clearShoppingInput struct {
	OnlyChecked *bool `json:"only_checked,omitempty" jsonschema:"only remove checked-off items (default true)"`
}

// ClearShoppingList removes shopping list entries.
func (h *handlers) ClearShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in clearShoppingInput) (*mcp.CallToolResult, any, error) {
	onlyChecked := in.OnlyChecked == nil || *in.OnlyChecked
	entries, err := h.shoppingEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	removed := 0
	for _, e := range entries {
		if onlyChecked && !e.Checked {
			continue
		}
		if _, err := h.c.Do(ctx, http.MethodDelete, fmt.Sprintf("shopping-list-entry/%d/", e.ID), nil, nil); err != nil {
			return nil, nil, fmt.Errorf("removing entry %d: %w", e.ID, err)
		}
		removed++
	}
	scope := "all items"
	if onlyChecked {
		scope = "checked-off items"
	}
	return textResult(fmt.Sprintf("removed %d %s from the shopping list", removed, scope))
}
