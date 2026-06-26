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
	EndDate  string `json:"end_date,omitempty"`
	MealType string `json:"meal_type,omitempty"`
	Recipe   string `json:"recipe,omitempty"`
	RecipeID int    `json:"recipe_id,omitempty"`
	Servings string `json:"servings,omitempty"`
	Title    string `json:"title,omitempty"`
}

func toMealPlanCard(m apiMealPlan) mealPlanCard {
	c := mealPlanCard{ID: m.ID, Date: m.FromDate, Servings: m.Servings.String(), Title: m.Title}
	if m.ToDate != "" && m.ToDate != m.FromDate {
		c.EndDate = m.ToDate
	}
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
	Recipe   string `json:"recipe,omitempty" jsonschema:"recipe name or id to plan; omit for a note-only entry"`
	EndDate  string `json:"end_date,omitempty" jsonschema:"end date YYYY-MM-DD for a multi-day entry; defaults to date"`
	Servings *int   `json:"servings,omitempty" jsonschema:"servings (default 1)"`
	Title    string `json:"title,omitempty" jsonschema:"label for the entry; defaults to the recipe name"`
	Note     string `json:"note,omitempty" jsonschema:"free-text note"`
}

// planMeal adds an entry to the meal-plan calendar.
func (h *handlers) planMeal(ctx context.Context, _ *mcp.CallToolRequest, in planMealInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Date) == "" {
		return nil, nil, fmt.Errorf("date is required (YYYY-MM-DD)")
	}
	if strings.TrimSpace(in.MealType) == "" {
		return nil, nil, fmt.Errorf("meal_type is required")
	}
	from := toDateTime(in.Date)
	to := from
	if in.EndDate != "" {
		to = toDateTime(in.EndDate)
	}
	servings := 1
	if in.Servings != nil {
		servings = *in.Servings
	}
	body := map[string]any{
		"from_date": from,
		"to_date":   to,
		"meal_type": map[string]any{"name": strings.TrimSpace(in.MealType)},
		"servings":  servings,
	}
	if in.Recipe != "" {
		id, err := h.resolveRecipe(ctx, in.Recipe)
		if err != nil {
			return nil, nil, err
		}
		body["recipe"] = id // bare int: Tandoor links the existing recipe
	}
	if in.Title != "" {
		body["title"] = in.Title
	}
	if in.Note != "" {
		body["note"] = in.Note
	}

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

// getMealPlan lists meal-plan entries, optionally within a date range.
func (h *handlers) getMealPlan(ctx context.Context, _ *mcp.CallToolRequest, in getMealPlanInput) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	// The list view filters on a __date lookup, so it wants plain dates.
	if in.From != "" {
		q.Set("from_date", dateOnly(in.From))
	}
	if in.To != "" {
		q.Set("to_date", dateOnly(in.To))
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

// ---- remove_meal_plan_entry ----

type removeMealPlanInput struct {
	ID int `json:"id" jsonschema:"meal plan entry id (from get_meal_plan)"`
}

// removeMealPlanEntry deletes a meal-plan entry.
func (h *handlers) removeMealPlanEntry(ctx context.Context, _ *mcp.CallToolRequest, in removeMealPlanInput) (*mcp.CallToolResult, any, error) {
	if _, err := h.c.Do(ctx, http.MethodDelete, fmt.Sprintf("meal-plan/%d/", in.ID), nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "removed", "id": in.ID})
}

// toDateTime turns YYYY-MM-DD into an ISO datetime (Tandoor's create fields are
// DateTimeFields and reject date-only input); values with a time pass through.
func toDateTime(d string) string {
	d = strings.TrimSpace(d)
	if d == "" || strings.Contains(d, "T") {
		return d
	}
	return d + "T00:00:00"
}

// dateOnly strips any time component, for the meal-plan list's __date filter.
func dateOnly(s string) string {
	d, _, _ := strings.Cut(strings.TrimSpace(s), "T")
	return d
}
