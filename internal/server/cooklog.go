package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxCookLogLimit = 200
	cookLogPageSize = 200
	cookLogPageCap  = 20
)

type getCookLogInput struct {
	Recipe    string `json:"recipe,omitempty" jsonschema:"recipe name or id to filter by"`
	From      string `json:"from,omitempty" jsonschema:"oldest cook date to include, YYYY-MM-DD"`
	To        string `json:"to,omitempty" jsonschema:"newest cook date to include, YYYY-MM-DD"`
	MinRating *int   `json:"min_rating,omitempty" jsonschema:"minimum rating, 0-5"`
	Limit     *int   `json:"limit,omitempty" jsonschema:"maximum logs to return (default 50)"`
}

type apiCookLog struct {
	ID        int             `json:"id"`
	Recipe    json.RawMessage `json:"recipe"`
	Servings  *int            `json:"servings"`
	Rating    *int            `json:"rating"`
	Comment   string          `json:"comment"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

type cookLogOut struct {
	ID        int    `json:"id"`
	RecipeID  int    `json:"recipe_id,omitempty"`
	Recipe    string `json:"recipe,omitempty"`
	Servings  *int   `json:"servings,omitempty"`
	Rating    *int   `json:"rating,omitempty"`
	Comment   string `json:"comment,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (h *handlers) getCookLog(ctx context.Context, _ *mcp.CallToolRequest, in getCookLogInput) (*mcp.CallToolResult, any, error) {
	limit := 50
	if in.Limit != nil {
		if *in.Limit <= 0 || *in.Limit > maxCookLogLimit {
			return nil, nil, fmt.Errorf("limit must be between 1 and %d", maxCookLogLimit)
		}
		limit = *in.Limit
	}
	if in.MinRating != nil && (*in.MinRating < 0 || *in.MinRating > 5) {
		return nil, nil, fmt.Errorf("min_rating must be between 0 and 5")
	}
	from, err := cleanOptionalShortText("from", in.From)
	if err != nil {
		return nil, nil, err
	}
	to, err := cleanOptionalShortText("to", in.To)
	if err != nil {
		return nil, nil, err
	}
	recipeID := 0
	if in.Recipe != "" {
		recipeID, err = h.resolveRecipe(ctx, in.Recipe)
		if err != nil {
			return nil, nil, err
		}
	}

	var logs []cookLogOut
	names := map[int]string{}
	truncated := false
	scanned := 0
	for page := 1; page <= cookLogPageCap && len(logs) < limit; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(cookLogPageSize))
		if recipeID > 0 {
			q.Set("recipe", strconv.Itoa(recipeID))
		}
		raw, err := h.c.Do(ctx, http.MethodGet, "cook-log/", q, nil)
		if err != nil {
			return nil, nil, err
		}
		var env listEnvelope
		paginated := json.Unmarshal(raw, &env) == nil && len(env.Results) > 0
		var batch []apiCookLog
		if paginated {
			if err := json.Unmarshal(env.Results, &batch); err != nil {
				return nil, nil, fmt.Errorf("decoding cook logs: %w", err)
			}
		} else if err := decodeList(raw, &batch); err != nil {
			return nil, nil, fmt.Errorf("decoding cook logs: %w", err)
		}
		scanned += len(batch)
		for _, log := range batch {
			out := toCookLogOut(log)
			if !cookLogMatches(out, from, to, in.MinRating) {
				continue
			}
			if out.RecipeID > 0 {
				names[out.RecipeID] = out.Recipe
			}
			logs = append(logs, out)
			if len(logs) == limit {
				if paginated && env.Next != nil && *env.Next != "" {
					truncated = true
				}
				break
			}
		}
		if !paginated || env.Next == nil || *env.Next == "" {
			break
		}
		if page == cookLogPageCap {
			truncated = true
		}
	}

	var warnings []string
	for id, name := range names {
		if name != "" {
			continue
		}
		_, recipe, _, err := h.fetchRecipe(ctx, id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not resolve recipe name for cook log recipe id %d: %v", id, err))
			continue
		}
		names[id] = recipe.Name
	}
	for i := range logs {
		if logs[i].Recipe == "" && logs[i].RecipeID > 0 {
			logs[i].Recipe = names[logs[i].RecipeID]
		}
	}

	summary := map[string]any{"count": len(logs), "scanned": scanned}
	if avg, ok := averageRating(logs); ok {
		summary["avg_rating"] = avg
	}
	return jsonResult(map[string]any{
		"logs":      logs,
		"summary":   summary,
		"truncated": truncated,
		"warnings":  warnings,
	})
}

func toCookLogOut(log apiCookLog) cookLogOut {
	id, name := recipeIDAndName(log.Recipe)
	return cookLogOut{ID: log.ID, RecipeID: id, Recipe: name, Servings: log.Servings, Rating: log.Rating, Comment: log.Comment, CreatedAt: log.CreatedAt}
}

func recipeIDAndName(raw json.RawMessage) (int, string) {
	var id int
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, ""
	}
	var obj named
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.ID, obj.Name
	}
	return 0, ""
}

func cookLogMatches(log cookLogOut, from, to string, minRating *int) bool {
	day := dateOnly(log.CreatedAt)
	if from != "" && day != "" && day < from {
		return false
	}
	if to != "" && day != "" && day > to {
		return false
	}
	if minRating != nil && (log.Rating == nil || *log.Rating < *minRating) {
		return false
	}
	return true
}

func averageRating(logs []cookLogOut) (float64, bool) {
	total := 0
	count := 0
	for _, log := range logs {
		if log.Rating != nil {
			total += *log.Rating
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return float64(total) / float64(count), true
}
