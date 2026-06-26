package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type apiMealPlan struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"`
	FromDate string  `json:"from_date"`
	ToDate   string  `json:"to_date"`
	Servings flexNum `json:"servings"`
	Recipe   *named  `json:"recipe"`
	MealType *named  `json:"meal_type"`
}

type mealPlanCard struct {
	ID       int    `json:"id"`
	Date     string `json:"date"`
	MealType string `json:"meal_type,omitempty"`
	Recipe   string `json:"recipe,omitempty"`
	RecipeID int    `json:"recipe_id,omitempty"`
	Servings string `json:"servings,omitempty"`
	Title    string `json:"title,omitempty"`
}

func toMealPlanCard(m apiMealPlan) mealPlanCard {
	c := mealPlanCard{ID: m.ID, Date: m.FromDate, Servings: m.Servings.String(), Title: m.Title}
	if m.MealType != nil {
		c.MealType = m.MealType.Name
	}
	if m.Recipe != nil {
		c.Recipe, c.RecipeID = m.Recipe.Name, m.Recipe.ID
	}
	return c
}

// ---- plan_meal ----

type planMealInput struct {
	Date     string `json:"date" jsonschema:"date for the meal in YYYY-MM-DD"`
	MealType string `json:"meal_type" jsonschema:"meal type name (Breakfast, Lunch, Dinner, ...); created if missing"`
	RecipeID *int   `json:"recipe_id,omitempty" jsonschema:"recipe to plan; omit for a note-only entry"`
	EndDate  string `json:"end_date,omitempty" jsonschema:"end date YYYY-MM-DD for a multi-day entry; defaults to date"`
	Servings *int   `json:"servings,omitempty"`
	Title    string `json:"title,omitempty" jsonschema:"label for the entry; defaults to the recipe name"`
	Note     string `json:"note,omitempty"`
}

// PlanMeal adds an entry to the meal-plan calendar.
func (h *handlers) PlanMeal(ctx context.Context, _ *mcp.CallToolRequest, in planMealInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Date) == "" {
		return nil, nil, fmt.Errorf("date is required (YYYY-MM-DD)")
	}
	if strings.TrimSpace(in.MealType) == "" {
		return nil, nil, fmt.Errorf("meal_type is required")
	}
	from := normalizeDateTime(in.Date)
	to := from
	if in.EndDate != "" {
		to = normalizeDateTime(in.EndDate)
	}
	body := map[string]any{
		"from_date": from,
		"to_date":   to,
		"meal_type": map[string]any{"name": strings.TrimSpace(in.MealType)},
	}
	if in.RecipeID != nil {
		body["recipe"] = map[string]any{"id": *in.RecipeID}
	}
	if in.Title != "" {
		body["title"] = in.Title
	}
	if in.Note != "" {
		body["note"] = in.Note
	}
	setInt(body, "servings", in.Servings)

	raw, err := h.c.Do(ctx, http.MethodPost, "meal-plan/", nil, body)
	if err != nil {
		return nil, nil, err
	}
	var m apiMealPlan
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("decoding meal plan: %w", err)
	}
	return jsonResult(map[string]any{"status": "planned", "entry": toMealPlanCard(m)})
}

// ---- get_meal_plan ----

type getMealPlanInput struct {
	From string `json:"from,omitempty" jsonschema:"start date YYYY-MM-DD (inclusive)"`
	To   string `json:"to,omitempty" jsonschema:"end date YYYY-MM-DD (inclusive)"`
}

// GetMealPlan lists meal-plan entries, optionally within a date range.
func (h *handlers) GetMealPlan(ctx context.Context, _ *mcp.CallToolRequest, in getMealPlanInput) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	if in.From != "" {
		q.Set("from_date", normalizeDateTime(in.From))
	}
	if in.To != "" {
		q.Set("to_date", normalizeDateTime(in.To))
	}
	raw, err := h.c.Do(ctx, http.MethodGet, "meal-plan/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	var plans []apiMealPlan
	if err := decodeList(raw, &plans); err != nil {
		return nil, nil, fmt.Errorf("decoding meal plans: %w", err)
	}
	cards := make([]mealPlanCard, 0, len(plans))
	for _, m := range plans {
		cards = append(cards, toMealPlanCard(m))
	}
	return jsonResult(cards)
}

// normalizeDateTime turns YYYY-MM-DD into an ISO datetime Tandoor accepts; values
// that already include a time component are passed through.
func normalizeDateTime(d string) string {
	d = strings.TrimSpace(d)
	if d == "" || strings.Contains(d, "T") {
		return d
	}
	return d + "T00:00:00"
}
