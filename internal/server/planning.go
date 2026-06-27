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
	Note     string  `json:"note"`
}

type mealPlanCard struct {
	ID         int    `json:"id"`
	Date       string `json:"date"`
	EndDate    string `json:"end_date,omitempty"`
	MealType   string `json:"meal_type,omitempty"`
	MealTypeID int    `json:"meal_type_id,omitempty"`
	Recipe     string `json:"recipe,omitempty"`
	RecipeID   int    `json:"recipe_id,omitempty"`
	Servings   string `json:"servings,omitempty"`
	Title      string `json:"title,omitempty"`
	Note       string `json:"note,omitempty"`
}

func toMealPlanCard(m apiMealPlan) mealPlanCard {
	c := mealPlanCard{ID: m.ID, Date: m.FromDate, Servings: m.Servings.String(), Title: m.Title, Note: m.Note}
	if m.ToDate != "" && m.ToDate != m.FromDate {
		c.EndDate = m.ToDate
	}
	if m.MealType != nil {
		c.MealType = m.MealType.Name
		c.MealTypeID = m.MealType.ID
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
	date, err := cleanRef("date", in.Date)
	if err != nil {
		return nil, nil, fmt.Errorf("date is required (YYYY-MM-DD)")
	}
	mealType, err := cleanName("meal_type", in.MealType)
	if err != nil {
		return nil, nil, err
	}
	from := toDateTime(date)
	to := from
	if in.EndDate != "" {
		endDate, err := cleanRef("end_date", in.EndDate)
		if err != nil {
			return nil, nil, err
		}
		to = toDateTime(endDate)
	}
	servings := 1
	if in.Servings != nil {
		if *in.Servings <= 0 {
			return nil, nil, fmt.Errorf("servings must be positive")
		}
		servings = *in.Servings
	}
	body := map[string]any{
		"from_date": from,
		"to_date":   to,
		"meal_type": map[string]any{"name": mealType},
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
		title, err := cleanOptionalShortText("title", in.Title)
		if err != nil {
			return nil, nil, err
		}
		body["title"] = title
	}
	if in.Note != "" {
		note, err := cleanOptionalFreeText("note", in.Note)
		if err != nil {
			return nil, nil, err
		}
		body["note"] = note
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
		from, err := cleanRef("from", in.From)
		if err != nil {
			return nil, nil, err
		}
		q.Set("from_date", dateOnly(from))
	}
	if in.To != "" {
		to, err := cleanRef("to", in.To)
		if err != nil {
			return nil, nil, err
		}
		q.Set("to_date", dateOnly(to))
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
	return jsonResult(map[string]any{"entries": cards})
}

// ---- update_meal_plan_entry ----

type updateMealPlanInput struct {
	ID       int     `json:"id" jsonschema:"meal plan entry id (from get_meal_plan)"`
	Date     *string `json:"date,omitempty" jsonschema:"new start date YYYY-MM-DD"`
	EndDate  *string `json:"end_date,omitempty" jsonschema:"new end date YYYY-MM-DD"`
	MealType *string `json:"meal_type,omitempty" jsonschema:"new meal type name"`
	Recipe   *string `json:"recipe,omitempty" jsonschema:"new recipe name or id; empty string clears the recipe"`
	Servings *int    `json:"servings,omitempty" jsonschema:"new servings"`
	Title    *string `json:"title,omitempty" jsonschema:"new title; empty string clears it"`
	Note     *string `json:"note,omitempty" jsonschema:"new free-text note; empty string clears it"`
}

func (h *handlers) updateMealPlanEntry(ctx context.Context, _ *mcp.CallToolRequest, in updateMealPlanInput) (*mcp.CallToolResult, any, error) {
	if err := validatePositiveID("meal plan entry id", in.ID); err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	if in.Date != nil {
		date, err := cleanRef("date", *in.Date)
		if err != nil {
			return nil, nil, fmt.Errorf("date is required (YYYY-MM-DD)")
		}
		body["from_date"] = toDateTime(date)
	}
	if in.EndDate != nil {
		endDate, err := cleanRef("end_date", *in.EndDate)
		if err != nil {
			return nil, nil, err
		}
		body["to_date"] = toDateTime(endDate)
	}
	if in.MealType != nil {
		mealType, err := cleanName("meal_type", *in.MealType)
		if err != nil {
			return nil, nil, err
		}
		body["meal_type"] = map[string]any{"name": mealType}
	}
	if in.Recipe != nil {
		if strings.TrimSpace(*in.Recipe) == "" {
			body["recipe"] = nil
		} else {
			id, err := h.resolveRecipe(ctx, *in.Recipe)
			if err != nil {
				return nil, nil, err
			}
			body["recipe"] = id
		}
	}
	if in.Servings != nil {
		if *in.Servings <= 0 {
			return nil, nil, fmt.Errorf("servings must be positive")
		}
		body["servings"] = *in.Servings
	}
	if in.Title != nil {
		title, err := cleanOptionalShortText("title", *in.Title)
		if err != nil {
			return nil, nil, err
		}
		body["title"] = title
	}
	if in.Note != nil {
		note, err := cleanOptionalFreeText("note", *in.Note)
		if err != nil {
			return nil, nil, err
		}
		body["note"] = note
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update: provide at least one field")
	}
	raw, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("meal-plan/%d/", in.ID), nil, body)
	if err != nil {
		return nil, nil, err
	}
	var m apiMealPlan
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("decoding meal plan: %w", err)
	}
	return jsonResult(map[string]any{"status": "updated", "entry": toMealPlanCard(m)})
}

// ---- remove_meal_plan_entry ----

type removeMealPlanInput struct {
	ID int `json:"id" jsonschema:"meal plan entry id (from get_meal_plan)"`
}

// removeMealPlanEntry deletes a meal-plan entry.
func (h *handlers) removeMealPlanEntry(ctx context.Context, _ *mcp.CallToolRequest, in removeMealPlanInput) (*mcp.CallToolResult, any, error) {
	if err := validatePositiveID("meal plan entry id", in.ID); err != nil {
		return nil, nil, err
	}
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
