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
	Food   string   `json:"food,omitempty" jsonschema:"food name (use foods to set several at once)"`
	Foods  []string `json:"foods,omitempty" jsonschema:"several food names to set in one call"`
	OnHand *bool    `json:"on_hand,omitempty" jsonschema:"true to mark on-hand (default), false to clear"`
}

// setFoodOnHand marks one or more foods as on-hand (in the pantry) or clears
// them. Multiple foods are processed independently: a failure on one is reported
// without aborting the rest.
func (h *handlers) setFoodOnHand(ctx context.Context, _ *mcp.CallToolRequest, in setOnhandInput) (*mcp.CallToolResult, any, error) {
	all := make([]string, 0, len(in.Foods)+1)
	all = append(all, in.Foods...)
	if strings.TrimSpace(in.Food) != "" {
		all = append(all, in.Food)
	}
	names := make([]string, 0, len(all))
	seen := map[string]bool{}
	for _, f := range all {
		f = strings.TrimSpace(f)
		if f == "" || seen[strings.ToLower(f)] {
			continue
		}
		seen[strings.ToLower(f)] = true
		names = append(names, f)
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("food (or foods) is required")
	}

	onHand := in.OnHand == nil || *in.OnHand
	done := make([]map[string]any, 0, len(names))
	var failures []string
	for _, f := range names {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err().Error())
			break
		}
		id, err := h.setOneFoodOnHand(ctx, f, onHand)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		done = append(done, map[string]any{"food": f, "id": id})
	}
	if len(done) == 0 {
		return nil, nil, fmt.Errorf("no foods updated: %s", strings.Join(failures, "; "))
	}
	out := map[string]any{"status": "updated", "on_hand": onHand, "foods": done}
	if len(failures) > 0 {
		out["status"] = "partial"
		out["failures"] = failures
	}
	return jsonResult(out)
}

// setOneFoodOnHand resolves (or, when marking on-hand, creates) a food by name
// and patches its on-hand flag.
func (h *handlers) setOneFoodOnHand(ctx context.Context, food string, onHand bool) (int, error) {
	var id int
	if onHand {
		got, err := h.getOrCreateID(ctx, "food", food)
		if err != nil {
			return 0, err
		}
		id = got
	} else {
		foundID, found, err := h.resolveUniqueExistingID(ctx, "food", "food", food)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, fmt.Errorf("not found; refusing to create a food while clearing on-hand state")
		}
		id = foundID
	}
	if _, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("food/%d/", id), nil, map[string]any{"food_onhand": onHand}); err != nil {
		return 0, err
	}
	return id, nil
}
