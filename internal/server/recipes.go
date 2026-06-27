package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

// maxImageBytes caps the size of a local image upload.
const maxImageBytes = 16 << 20

const maxFindRecipesLimit = 100

// ---- find_recipes ----

type findRecipesInput struct {
	Text        string   `json:"text,omitempty" jsonschema:"words to search in recipe name and description"`
	Keywords    []string `json:"keywords,omitempty" jsonschema:"only recipes tagged with ALL of these keyword names"`
	Ingredients []string `json:"ingredients,omitempty" jsonschema:"only recipes using ALL of these food/ingredient names"`
	Book        string   `json:"book,omitempty" jsonschema:"only recipes in this recipe book (by name)"`
	MinRating   *int     `json:"min_rating,omitempty" jsonschema:"minimum star rating, 0-5"`
	MakeableNow *bool    `json:"makeable_now,omitempty" jsonschema:"only recipes you can make from foods marked on-hand"`
	Newest      *bool    `json:"newest,omitempty" jsonschema:"return the most recently created recipes first"`
	Random      *bool    `json:"random,omitempty" jsonschema:"randomize the order"`
	Limit       *int     `json:"limit,omitempty" jsonschema:"maximum recipes to return (default 25)"`
}

type findRecipesOutput struct {
	Count    int          `json:"count"`
	Returned int          `json:"returned"`
	Warnings []string     `json:"warnings,omitempty"`
	Recipes  []recipeCard `json:"recipes"`
}

// findRecipes searches recipes using names (resolved to ids) and returns compact cards.
func (h *handlers) findRecipes(ctx context.Context, _ *mcp.CallToolRequest, in findRecipesInput) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	addStr(q, "query", in.Text)

	if len(in.Keywords) > 0 {
		ids, err := h.resolveRequiredIDs(ctx, "keyword", "keyword", in.Keywords)
		if err != nil {
			return nil, nil, err
		}
		addInts(q, "keywords_and", ids) // _and = match ALL (bare param is OR)
	}
	if len(in.Ingredients) > 0 {
		ids, err := h.resolveRequiredIDs(ctx, "food", "ingredient", in.Ingredients)
		if err != nil {
			return nil, nil, err
		}
		addInts(q, "foods_and", ids)
	}
	if in.Book != "" {
		id, err := h.resolveRecipeBookID(ctx, in.Book)
		if err != nil {
			return nil, nil, err
		}
		q.Set("books_and", strconv.Itoa(id))
	}
	addInt(q, "rating_gte", in.MinRating)
	addBool(q, "makenow", in.MakeableNow)
	addBool(q, "random", in.Random)
	if in.Newest != nil && *in.Newest {
		q.Set("sort_order", "-created_at")
	}
	limit := 25
	if in.Limit != nil {
		if *in.Limit <= 0 || *in.Limit > maxFindRecipesLimit {
			return nil, nil, fmt.Errorf("limit must be between 1 and %d", maxFindRecipesLimit)
		}
		limit = *in.Limit
	}
	q.Set("page_size", strconv.Itoa(limit))

	raw, err := h.c.Do(ctx, http.MethodGet, "recipe/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	var env listEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("decoding recipe list: %w", err)
	}
	var recipes []apiRecipe
	if err := json.Unmarshal(env.Results, &recipes); err != nil {
		return nil, nil, fmt.Errorf("decoding recipes: %w", err)
	}
	out := findRecipesOutput{Count: env.Count, Returned: len(recipes)}
	for _, r := range recipes {
		out.Recipes = append(out.Recipes, toCard(r))
	}
	return jsonResult(out)
}

// ---- get_recipe ----

type getRecipeInput struct {
	Recipe   string `json:"recipe" jsonschema:"recipe name or numeric id"`
	Servings *int   `json:"servings,omitempty" jsonschema:"re-scale ingredient amounts to this serving count"`
}

type getRecipeOutput struct {
	ID             int           `json:"id"`
	Name           string        `json:"name"`
	Rating         string        `json:"rating,omitempty"`
	Servings       string        `json:"servings,omitempty"`
	StoredServings string        `json:"stored_servings,omitempty"`
	WorkingTimeMin string        `json:"working_time_min,omitempty"`
	WaitingTimeMin string        `json:"waiting_time_min,omitempty"`
	Keywords       []string      `json:"keywords,omitempty"`
	SourceURL      string        `json:"source_url,omitempty"`
	Nutrition      *nutritionOut `json:"nutrition,omitempty"`
	Properties     []propertyOut `json:"properties,omitempty"`
	Steps          []stepOut     `json:"steps"`
	Warnings       []string      `json:"warnings,omitempty"`
	EditRevision   string        `json:"edit_revision"`
	Markdown       string        `json:"markdown"`
}

// getRecipe returns one recipe as structured fields plus a readable Markdown view.
func (h *handlers) getRecipe(ctx context.Context, _ *mcp.CallToolRequest, in getRecipeInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	_, r, revision, err := h.fetchRecipe(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rendered := cloneRecipe(r)
	var warnings []string
	if in.Servings != nil && *in.Servings > 0 && r.Servings.Set && r.Servings.Value > 0 {
		scaleAmounts(&rendered, float64(*in.Servings)/r.Servings.Value)
		rendered.Servings = flexNum{Set: true, Value: float64(*in.Servings)}
		warnings = append(warnings, "markdown amounts are scaled for display; structured steps keep stored amounts for safe editing")
	}
	return jsonResult(getRecipeOutput{
		ID: r.ID, Name: r.Name, Rating: r.Rating.String(), Servings: rendered.Servings.String(), StoredServings: r.Servings.String(),
		WorkingTimeMin: r.WorkingTime.String(), WaitingTimeMin: r.WaitingTime.String(),
		Keywords: keywordNames(r.Keywords), SourceURL: r.SourceURL,
		// Nutrition is shown as stored, never re-scaled: its basis (per-recipe vs
		// per-serving) is configuration-dependent, so scaling it would mislead.
		Nutrition: toNutrition(r), Properties: toProperties(r),
		Steps: toStepOuts(r), Warnings: warnings, EditRevision: revision, Markdown: renderRecipe(rendered),
	})
}

// ---- create_recipe ----

type createRecipeInput struct {
	Name         string            `json:"name" jsonschema:"recipe name"`
	Description  string            `json:"description,omitempty" jsonschema:"free-text description"`
	Servings     *int              `json:"servings,omitempty" jsonschema:"number of servings the ingredient amounts make"`
	WorkingTime  *int              `json:"working_time,omitempty" jsonschema:"active/prep time in minutes"`
	WaitingTime  *int              `json:"waiting_time,omitempty" jsonschema:"waiting/cooking time in minutes"`
	SourceURL    string            `json:"source_url,omitempty" jsonschema:"original source URL, if any"`
	Keywords     []string          `json:"keywords,omitempty" jsonschema:"tag names; created if they do not exist"`
	Ingredients  []ingredientInput `json:"ingredients,omitempty" jsonschema:"ingredients for a simple one-step recipe; for multi-step recipes use steps instead"`
	Instructions string            `json:"instructions,omitempty" jsonschema:"instruction text for the single step when using the top-level ingredients field"`
	Steps        []stepInput       `json:"steps,omitempty" jsonschema:"ordered steps each with their own ingredients (use instead of top-level ingredients/instructions)"`
}

type createRecipeOutput struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Steps       int      `json:"steps"`
	Keywords    []string `json:"keywords,omitempty"`
	Ingredients []string `json:"ingredients,omitempty"` // as stored, for quantity verification without a get_recipe round-trip
}

// createRecipe creates a recipe, splitting quantities out of natural-language
// ingredient lines and get-or-creating foods, units and keywords by name.
func (h *handlers) createRecipe(ctx context.Context, _ *mcp.CallToolRequest, in createRecipeInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	// Refuse the ambiguous "both" case rather than silently dropping the
	// top-level ingredients/instructions in favor of steps[].
	if len(in.Steps) > 0 && (len(in.Ingredients) > 0 || in.Instructions != "") {
		return nil, nil, fmt.Errorf("provide top-level ingredients/instructions for a simple recipe OR steps[] for a multi-step one, not both")
	}
	stepInputs := in.Steps
	if len(stepInputs) == 0 && (len(in.Ingredients) > 0 || in.Instructions != "") {
		stepInputs = []stepInput{{Instruction: in.Instructions, Ingredients: in.Ingredients}}
	}
	steps, err := h.buildSteps(ctx, stepInputs)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"name":     in.Name,
		"internal": true,
		"keywords": keywordObjects(in.Keywords),
		"steps":    steps,
	}
	if in.Description != "" {
		body["description"] = in.Description
	}
	if in.SourceURL != "" {
		body["source_url"] = in.SourceURL
	}
	setInt(body, "servings", in.Servings)
	setInt(body, "working_time", in.WorkingTime)
	setInt(body, "waiting_time", in.WaitingTime)

	raw, err := h.c.Do(ctx, http.MethodPost, "recipe/", nil, body)
	if err != nil {
		var unknown *tandoor.OutcomeUnknownError
		if errors.As(err, &unknown) {
			extra := map[string]any{"operation": "create_recipe", "name": in.Name}
			if ids, lookupErr := h.recipeCandidateIDsByName(ctx, in.Name); lookupErr == nil && len(ids) > 0 {
				extra["candidate_recipe_ids"] = ids
			} else if lookupErr != nil {
				extra["candidate_lookup_error"] = lookupErr.Error()
			}
			return outcomeUnknownResult(unknown, extra)
		}
		return nil, nil, err
	}
	var created apiRecipe
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, nil, fmt.Errorf("decoding created recipe: %w", err)
	}
	var ingredients []string
	for _, st := range created.Steps {
		for _, ing := range st.Ingredients {
			if line := formatIngredient(ing); line != "" {
				ingredients = append(ingredients, line)
			}
		}
	}
	return jsonResult(createRecipeOutput{
		ID: created.ID, Name: created.Name, Status: "created",
		Steps: len(created.Steps), Keywords: keywordNames(created.Keywords),
		Ingredients: ingredients,
	})
}

// ---- import_recipe_from_url ----

type importRecipeInput struct {
	URL          string `json:"url" jsonschema:"recipe web page URL to import (http/https public host)"`
	Save         *bool  `json:"save,omitempty" jsonschema:"save to your collection (default true); false returns a parsed preview only when Tandoor returns a parsed recipe without saving server-side"`
	AllowPartial *bool  `json:"allow_partial,omitempty" jsonschema:"save even if Tandoor's parser drops ingredients without a parsed food (default false)"`
}

type sourceRecipe struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Servings    *int   `json:"servings"`
	WorkingTime *int   `json:"working_time"`
	WaitingTime *int   `json:"waiting_time"`
	SourceURL   string `json:"source_url"`
	ImageURL    string `json:"image_url"`
	Keywords    []struct {
		Name string `json:"name"`
	} `json:"keywords"`
	Steps []struct {
		Instruction string `json:"instruction"`
		Ingredients []struct {
			Amount       *float64 `json:"amount"`
			Food         *named   `json:"food"`
			Unit         *named   `json:"unit"`
			Note         string   `json:"note"`
			OriginalText string   `json:"original_text"`
		} `json:"ingredients"`
	} `json:"steps"`
}

type importRecipeOutput struct {
	ID                 int      `json:"id,omitempty"`
	Name               string   `json:"name,omitempty"`
	Status             string   `json:"status"`
	Source             string   `json:"source,omitempty"`
	ImageURL           string   `json:"image_url,omitempty"`
	Duplicates         []named  `json:"duplicates,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	DroppedIngredients []string `json:"dropped_ingredients,omitempty"`
	Partial            bool     `json:"partial,omitempty"`
	Markdown           string   `json:"markdown,omitempty"`
}

// importRecipeFromURL scrapes a recipe and (by default) saves it.
func (h *handlers) importRecipeFromURL(ctx context.Context, _ *mcp.CallToolRequest, in importRecipeInput) (*mcp.CallToolResult, any, error) {
	if err := validateHTTPURL(in.URL); err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodPost, "recipe-from-source/", nil, map[string]any{"url": in.URL})
	if err != nil {
		var unknown *tandoor.OutcomeUnknownError
		if errors.As(err, &unknown) {
			return outcomeUnknownResult(unknown, map[string]any{"operation": "import_recipe_from_url", "source": in.URL})
		}
		if msg, ok := importErrorMessage(err); ok {
			return nil, nil, fmt.Errorf("could not import: %s", msg)
		}
		return nil, nil, err
	}
	var resp struct {
		Recipe     json.RawMessage `json:"recipe"`
		RecipeID   *int            `json:"recipe_id"`
		Error      bool            `json:"error"`
		Msg        string          `json:"msg"`
		Duplicates []named         `json:"duplicates"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("decoding scrape response: %w", err)
	}
	save := in.Save == nil || *in.Save
	// YouTube / Tandoor-share URLs are saved server-side and return only an id.
	if resp.RecipeID != nil && *resp.RecipeID > 0 {
		if !save {
			return nil, nil, fmt.Errorf("preview is not available for this source: Tandoor already imported recipe id %d server-side", *resp.RecipeID)
		}
		return jsonResult(importRecipeOutput{ID: *resp.RecipeID, Status: "imported", Source: in.URL, Duplicates: resp.Duplicates})
	}
	if resp.Error || len(resp.Recipe) == 0 || string(resp.Recipe) == "null" {
		msg := resp.Msg
		if msg == "" {
			msg = "no recipe found at that URL"
		}
		return nil, nil, fmt.Errorf("could not import: %s", msg)
	}
	var sr sourceRecipe
	if err := json.Unmarshal(resp.Recipe, &sr); err != nil {
		return nil, nil, fmt.Errorf("decoding parsed recipe: %w", err)
	}

	if !save {
		return jsonResult(importRecipeOutput{
			Status: "preview", Name: sr.Name, Source: in.URL, ImageURL: sr.ImageURL,
			Duplicates: resp.Duplicates, Markdown: renderSourceRecipe(sr),
		})
	}

	body, warnings := sourceRecipeToBody(sr, in.URL)
	if len(warnings) > 0 && (in.AllowPartial == nil || !*in.AllowPartial) {
		return nil, nil, fmt.Errorf("refusing to save parsed recipe because %d ingredient(s) would be dropped; call with save=false to preview or allow_partial=true to save anyway: %s", len(warnings), strings.Join(warnings, "; "))
	}
	created, err := h.c.Do(ctx, http.MethodPost, "recipe/", nil, body)
	if err != nil {
		var unknown *tandoor.OutcomeUnknownError
		if errors.As(err, &unknown) {
			extra := map[string]any{"operation": "import_recipe_from_url_create", "source": in.URL, "name": sr.Name}
			if ids, lookupErr := h.recipeCandidateIDsByName(ctx, sr.Name); lookupErr == nil && len(ids) > 0 {
				extra["candidate_recipe_ids"] = ids
			} else if lookupErr != nil {
				extra["candidate_lookup_error"] = lookupErr.Error()
			}
			return outcomeUnknownResult(unknown, extra)
		}
		return nil, nil, err
	}
	var rec apiRecipe
	if err := json.Unmarshal(created, &rec); err != nil {
		return nil, nil, fmt.Errorf("decoding imported recipe: %w", err)
	}
	status := "imported"
	partial := false
	if len(warnings) > 0 {
		status = "imported_partial"
		partial = true
	}
	return jsonResult(importRecipeOutput{
		ID: rec.ID, Name: rec.Name, Status: status, Source: in.URL,
		ImageURL: sr.ImageURL, Duplicates: resp.Duplicates, Warnings: warnings,
		DroppedIngredients: warnings, Partial: partial,
	})
}

// ---- update_recipe ----

type updateRecipeInput struct {
	Recipe           string   `json:"recipe" jsonschema:"recipe name or numeric id"`
	ExpectedRevision string   `json:"expected_revision,omitempty" jsonschema:"edit_revision from get_recipe; required for keyword edits"`
	Name             *string  `json:"name,omitempty" jsonschema:"new name"`
	Description      *string  `json:"description,omitempty" jsonschema:"new description"`
	Servings         *int     `json:"servings,omitempty" jsonschema:"new serving count"`
	WorkingTime      *int     `json:"working_time,omitempty" jsonschema:"prep time in minutes"`
	WaitingTime      *int     `json:"waiting_time,omitempty" jsonschema:"cooking/waiting time in minutes"`
	SourceURL        *string  `json:"source_url,omitempty" jsonschema:"new source URL"`
	AddKeywords      []string `json:"add_keywords,omitempty" jsonschema:"keyword names to add"`
	RemoveKeywords   []string `json:"remove_keywords,omitempty" jsonschema:"keyword names to remove"`
}

// updateRecipe applies targeted edits without resending the whole recipe.
func (h *handlers) updateRecipe(ctx context.Context, _ *mcp.CallToolRequest, in updateRecipeInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	if in.Name != nil {
		body["name"] = *in.Name
	}
	if in.Description != nil {
		body["description"] = *in.Description
	}
	if in.SourceURL != nil {
		body["source_url"] = *in.SourceURL
	}
	setInt(body, "servings", in.Servings)
	setInt(body, "working_time", in.WorkingTime)
	setInt(body, "waiting_time", in.WaitingTime)

	if len(in.AddKeywords) > 0 || len(in.RemoveKeywords) > 0 {
		if strings.TrimSpace(in.ExpectedRevision) == "" {
			return nil, nil, fmt.Errorf("expected_revision from get_recipe is required for keyword edits")
		}
		_, r, revision, err := h.fetchRecipe(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if revision != strings.TrimSpace(in.ExpectedRevision) {
			return nil, nil, fmt.Errorf("recipe changed since get_recipe; refresh it and retry with the new edit_revision")
		}
		names := mergedKeywordNamesFromRecipe(r, in.AddKeywords, in.RemoveKeywords)
		body["keywords"] = keywordObjects(names)
	} else if strings.TrimSpace(in.ExpectedRevision) != "" {
		if err := h.checkRecipeRevision(ctx, id, in.ExpectedRevision); err != nil {
			return nil, nil, err
		}
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update: provide at least one field")
	}
	return h.patchRecipe(ctx, id, body, "updated")
}

// ---- set_recipe_steps ----

type setRecipeStepsInput struct {
	Recipe           string      `json:"recipe" jsonschema:"recipe name or numeric id"`
	ExpectedRevision string      `json:"expected_revision" jsonschema:"edit_revision from get_recipe"`
	Steps            []stepInput `json:"steps" jsonschema:"the full non-empty ordered list of steps and ingredients, replacing the existing ones"`
}

// setRecipeSteps replaces a recipe's steps/ingredients (re-describe to edit).
func (h *handlers) setRecipeSteps(ctx context.Context, _ *mcp.CallToolRequest, in setRecipeStepsInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.ExpectedRevision) == "" {
		return nil, nil, fmt.Errorf("expected_revision from get_recipe is required")
	}
	if err := h.checkRecipeRevision(ctx, id, in.ExpectedRevision); err != nil {
		return nil, nil, err
	}
	if len(in.Steps) == 0 {
		return nil, nil, fmt.Errorf("steps must be a non-empty list; this tool does not clear all recipe steps")
	}
	steps, err := h.buildSteps(ctx, in.Steps)
	if err != nil {
		return nil, nil, err
	}
	return h.patchRecipe(ctx, id, map[string]any{"steps": steps}, "updated")
}

func (h *handlers) patchRecipe(ctx context.Context, id int, body map[string]any, status string) (*mcp.CallToolResult, any, error) {
	raw, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("recipe/%d/", id), nil, body)
	if err != nil {
		return nil, nil, err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, fmt.Errorf("decoding updated recipe: %w", err)
	}
	return jsonResult(map[string]any{"status": status, "recipe": toCard(r)})
}

func (h *handlers) fetchRecipe(ctx context.Context, id int) (json.RawMessage, apiRecipe, string, error) {
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/", id), nil, nil)
	if err != nil {
		return nil, apiRecipe{}, "", err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, apiRecipe{}, "", fmt.Errorf("decoding recipe: %w", err)
	}
	return raw, r, recipeRevision(raw), nil
}

func recipeRevision(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h *handlers) checkRecipeRevision(ctx context.Context, id int, expected string) error {
	_, _, revision, err := h.fetchRecipe(ctx, id)
	if err != nil {
		return err
	}
	if revision != strings.TrimSpace(expected) {
		return fmt.Errorf("recipe changed since get_recipe; refresh it and retry with the new edit_revision")
	}
	return nil
}

func (h *handlers) recipeCandidateIDsByName(ctx context.Context, name string) ([]int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("query", name)
	q.Set("page_size", "10")
	raw, err := h.c.Do(ctx, http.MethodGet, "recipe/", q, nil)
	if err != nil {
		return nil, err
	}
	var recipes []apiRecipe
	if err := decodeList(raw, &recipes); err != nil {
		return nil, fmt.Errorf("decoding candidate recipes: %w", err)
	}
	var ids []int
	for _, r := range recipes {
		if strings.EqualFold(strings.TrimSpace(r.Name), name) {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

func mergedKeywordNamesFromRecipe(r apiRecipe, add, remove []string) []string {
	set := map[string]string{} // lower -> display
	for _, k := range r.Keywords {
		set[strings.ToLower(k.display())] = k.display()
	}
	for _, n := range add {
		if n = strings.TrimSpace(n); n != "" {
			set[strings.ToLower(n)] = n
		}
	}
	for _, n := range remove {
		delete(set, strings.ToLower(strings.TrimSpace(n)))
	}
	names := make([]string, 0, len(set))
	for _, v := range set {
		names = append(names, v)
	}
	return names
}

// ---- delete_recipe ----

type deleteRecipeInput struct {
	Recipe string `json:"recipe" jsonschema:"recipe name or numeric id"`
}

// deleteRecipe deletes a recipe.
func (h *handlers) deleteRecipe(ctx context.Context, _ *mcp.CallToolRequest, in deleteRecipeInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	if _, err := h.c.Do(ctx, http.MethodDelete, fmt.Sprintf("recipe/%d/", id), nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "deleted", "id": id})
}

// ---- set_recipe_image ----

type recipeImageInput struct {
	Recipe    string `json:"recipe" jsonschema:"recipe name or numeric id"`
	ImagePath string `json:"image_path,omitempty" jsonschema:"local image file path; only allowed within the server's configured image directory"`
	ImageURL  string `json:"image_url,omitempty" jsonschema:"public remote image URL (http/https; no credentials, localhost, private, link-local, or internal hosts) for Tandoor to fetch and store"`
}

// setRecipeImage sets a recipe image from a (gated) local file or a remote URL.
func (h *handlers) setRecipeImage(ctx context.Context, _ *mcp.CallToolRequest, in recipeImageInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	path := fmt.Sprintf("recipe/%d/image/", id)
	hasPath := strings.TrimSpace(in.ImagePath) != ""
	hasURL := strings.TrimSpace(in.ImageURL) != ""
	if hasPath == hasURL {
		return nil, nil, fmt.Errorf("provide exactly one of image_path or image_url")
	}
	switch {
	case hasPath:
		f, size, err := h.openSafeImage(in.ImagePath)
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = f.Close() }()
		if size > maxImageBytes {
			return nil, nil, fmt.Errorf("image is %d bytes, larger than the %d byte limit", size, maxImageBytes)
		}
		if _, err := h.c.Upload(ctx, http.MethodPut, path, nil, "image", filepath.Base(f.Name()), io.LimitReader(f, maxImageBytes)); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"status": "image_set", "id": id, "from": "file"})
	case hasURL:
		if err := validateHTTPURL(in.ImageURL); err != nil {
			return nil, nil, err
		}
		if _, err := h.c.Upload(ctx, http.MethodPut, path, map[string]string{"image_url": in.ImageURL}, "", "", nil); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"status": "image_set", "id": id, "from": "url"})
	}
	return nil, nil, fmt.Errorf("provide exactly one of image_path or image_url")
}

// errImagePathDenied is a single, uniform error for any local image path that is
// missing or outside the allowed directory, so the tool can't be used to probe
// which host files exist.
var errImagePathDenied = errors.New("image_path is not an accessible file within the allowed image directory")

// safeImagePath validates a local image path against the configured allow-dir.
func (h *handlers) safeImagePath(p string) (string, error) {
	if h.imageDir == "" {
		return "", fmt.Errorf("local image upload is disabled; set TANDOOR_IMAGE_DIR to a directory to allow it, or use image_url")
	}
	root, err := filepath.EvalSymlinks(h.imageDir)
	if err != nil {
		return "", fmt.Errorf("configured image directory %q is not accessible: %w", h.imageDir, err)
	}
	full, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errImagePathDenied
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errImagePathDenied
	}
	return full, nil
}

func (h *handlers) openSafeImage(p string) (*os.File, int64, error) {
	full, err := h.safeImagePath(p)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, errImagePathDenied
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, errImagePathDenied
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, errImagePathDenied
	}
	return f, fi.Size(), nil
}

// ---- find_related_recipes ----

type recipeRefInput struct {
	Recipe string `json:"recipe" jsonschema:"recipe name or numeric id"`
}

// findRelatedRecipes lists recipes related to the given recipe.
func (h *handlers) findRelatedRecipes(ctx context.Context, _ *mcp.CallToolRequest, in recipeRefInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/related/", id), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var recipes []apiRecipe
	if err := json.Unmarshal(raw, &recipes); err != nil {
		return nil, nil, fmt.Errorf("decoding related recipes: %w", err)
	}
	cards := make([]recipeCard, 0, len(recipes))
	for _, r := range recipes {
		cards = append(cards, toCard(r))
	}
	return jsonResult(map[string]any{"recipes": cards})
}

// ---- log_cooked ----

type logCookedInput struct {
	Recipe   string `json:"recipe" jsonschema:"recipe name or numeric id that was cooked"`
	Rating   *int   `json:"rating,omitempty" jsonschema:"rating to record, 0-5"`
	Servings *int   `json:"servings,omitempty" jsonschema:"servings made"`
	Comment  string `json:"comment,omitempty" jsonschema:"optional note"`
}

// logCooked records a cook-log entry (which is also how a recipe gets rated).
func (h *handlers) logCooked(ctx context.Context, _ *mcp.CallToolRequest, in logCookedInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{"recipe": id}
	setInt(body, "rating", in.Rating)
	setInt(body, "servings", in.Servings)
	if in.Comment != "" {
		body["comment"] = in.Comment
	}
	if _, err := h.c.Do(ctx, http.MethodPost, "cook-log/", nil, body); err != nil {
		return nil, nil, err
	}
	out := map[string]any{"status": "logged", "recipe_id": id}
	if in.Rating != nil {
		out["rating"] = *in.Rating
	}
	return jsonResult(out)
}

// ---- shared builders ----

func keywordObjects(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, map[string]any{"name": n})
		}
	}
	return out
}

func keywordNames(ks []apiKeyword) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.display())
	}
	return out
}

func setInt(body map[string]any, key string, v *int) {
	if v != nil {
		body[key] = *v
	}
}

// validateHTTPURL rejects URLs that aren't public http/https targets before any
// agent-supplied URL is handed to Tandoor's server-side fetcher.
func validateHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("a URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must be http or https, got %q", u.Scheme)
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("URL must include a host")
	}
	if u.User != nil {
		return fmt.Errorf("URL must not include credentials")
	}
	if isUnsafeFetchHost(host) {
		return fmt.Errorf("URL host %q is not allowed for server-side fetching", host)
	}
	return nil
}

func isUnsafeFetchHost(host string) bool {
	switch host {
	case "localhost", "localhost.localdomain":
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".corp"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

// importErrorMessage extracts Tandoor's friendly failure message from a 400.
func importErrorMessage(err error) (string, bool) {
	var apiErr *tandoor.APIError
	if !errors.As(err, &apiErr) {
		return "", false
	}
	var body struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &body) == nil && strings.TrimSpace(body.Msg) != "" {
		return body.Msg, true
	}
	return "", false
}

// sourceRecipeToBody maps a scraped recipe into a recipe-create payload, returning
// warnings for any ingredients that could not be attributed to a food.
func sourceRecipeToBody(sr sourceRecipe, sourceURL string) (map[string]any, []string) {
	var warnings []string
	steps := make([]map[string]any, 0, len(sr.Steps))
	for si, st := range sr.Steps {
		ings := make([]map[string]any, 0, len(st.Ingredients))
		for _, ing := range st.Ingredients {
			in := ingredientInput{Note: ing.Note}
			if ing.Amount != nil {
				in.Amount = ing.Amount
			}
			if ing.Unit != nil {
				in.Unit = ing.Unit.Name
			}
			if ing.Food != nil {
				in.Food = ing.Food.Name
			}
			if in.Food == "" {
				if t := strings.TrimSpace(ing.OriginalText); t != "" {
					warnings = append(warnings, fmt.Sprintf("dropped ingredient with no food: %q", t))
				}
				continue
			}
			payload, err := buildIngredient(in, nil)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("dropped ingredient %q: %v", ing.OriginalText, err))
				continue
			}
			if ing.OriginalText != "" {
				payload["original_text"] = ing.OriginalText
			}
			ings = append(ings, payload)
		}
		steps = append(steps, buildStep(st.Instruction, si, nil, ings))
	}
	names := make([]string, 0, len(sr.Keywords))
	for _, k := range sr.Keywords {
		names = append(names, k.Name)
	}
	src := sourceURL
	if src == "" {
		src = sr.SourceURL
	}
	body := map[string]any{
		"name":       sr.Name,
		"internal":   true,
		"source_url": src,
		"keywords":   keywordObjects(names),
		"steps":      steps,
	}
	if sr.Description != "" {
		body["description"] = sr.Description
	}
	setInt(body, "servings", sr.Servings)
	setInt(body, "working_time", sr.WorkingTime)
	setInt(body, "waiting_time", sr.WaitingTime)
	return body, warnings
}

// renderSourceRecipe renders a scraped (unsaved) recipe for preview.
func renderSourceRecipe(sr sourceRecipe) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", sr.Name)
	if sr.SourceURL != "" {
		fmt.Fprintf(&b, "Source: %s\n", sr.SourceURL)
	}
	if sr.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", sr.Description)
	}
	for si, st := range sr.Steps {
		fmt.Fprintf(&b, "\n## Step %d\n", si+1)
		for _, ing := range st.Ingredients {
			line := strings.TrimSpace(ing.OriginalText)
			if line == "" && ing.Food != nil {
				line = ing.Food.Name
			}
			if line != "" {
				fmt.Fprintf(&b, "- %s\n", line)
			}
		}
		if st.Instruction != "" {
			fmt.Fprintf(&b, "\n%s\n", st.Instruction)
		}
	}
	return b.String()
}
