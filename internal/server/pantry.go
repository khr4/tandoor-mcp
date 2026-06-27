package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pantryScanPages bounds how many food pages get_pantry will scan, since the API
// offers no server-side on-hand filter.
const (
	pantryScanPages = 10
	pantryPageSize  = 200
)

type apiFoodOnhand struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	FoodOnhand bool   `json:"food_onhand"`
}

type pantryOutput struct {
	OnHand    []named `json:"on_hand"`
	Truncated bool    `json:"truncated,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// ---- get_pantry ----

// getPantry lists foods currently marked on-hand. It scans the food list because
// Tandoor exposes no on-hand filter; very large food catalogs may be truncated.
func (h *handlers) getPantry(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	out := pantryOutput{OnHand: []named{}}
	for page := 1; page <= pantryScanPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(pantryPageSize))
		raw, err := h.c.Do(ctx, http.MethodGet, "food/", q, nil)
		if err != nil {
			return nil, nil, err
		}
		var env listEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, nil, fmt.Errorf("decoding food list: %w", err)
		}
		var foods []apiFoodOnhand
		if err := json.Unmarshal(env.Results, &foods); err != nil {
			return nil, nil, fmt.Errorf("decoding foods: %w", err)
		}
		for _, f := range foods {
			if f.FoodOnhand {
				out.OnHand = append(out.OnHand, named{ID: f.ID, Name: f.Name})
			}
		}
		if env.Next == nil || *env.Next == "" {
			return jsonResult(out)
		}
	}
	out.Truncated = true
	out.Note = fmt.Sprintf("scanned the first %d pages of foods; more may exist", pantryScanPages)
	return jsonResult(out)
}

// ---- set_food_on_hand ----

type setOnhandInput struct {
	Food   string `json:"food" jsonschema:"food name"`
	OnHand *bool  `json:"on_hand,omitempty" jsonschema:"true to mark on-hand (default), false to clear"`
}

// setFoodOnHand marks a food as on-hand (in the pantry) or clears it.
func (h *handlers) setFoodOnHand(ctx context.Context, _ *mcp.CallToolRequest, in setOnhandInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Food) == "" {
		return nil, nil, fmt.Errorf("food is required")
	}
	onHand := in.OnHand == nil || *in.OnHand
	var id int
	if onHand {
		var err error
		id, err = h.getOrCreateID(ctx, "food", in.Food)
		if err != nil {
			return nil, nil, err
		}
	} else {
		foundID, found, err := h.resolveUniqueExistingID(ctx, "food", "food", in.Food)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, fmt.Errorf("food %q not found; refusing to create a food while clearing on-hand state", in.Food)
		}
		id = foundID
	}
	if _, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("food/%d/", id), nil, map[string]any{"food_onhand": onHand}); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "updated", "food": in.Food, "id": id, "on_hand": onHand})
}
