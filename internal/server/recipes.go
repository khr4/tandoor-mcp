package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

// maxImageBytes caps the size of a local image upload.
const maxImageBytes = 16 << 20

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

	var warnings []string
	if len(in.Keywords) > 0 {
		ids, w := h.resolveExistingIDs(ctx, "keyword", "keyword", in.Keywords)
		addInts(q, "keywords_and", ids) // _and = match ALL (bare param is OR)
		warnings = append(warnings, w...)
	}
	if len(in.Ingredients) > 0 {
		ids, w := h.resolveExistingIDs(ctx, "food", "ingredient", in.Ingredients)
		addInts(q, "foods_and", ids)
		warnings = append(warnings, w...)
	}
	if in.Book != "" {
		id, found, err := h.resolveExistingID(ctx, "recipe-book", in.Book)
		if err != nil {
			return nil, nil, err
		}
		if found {
			q.Set("books_and", strconv.Itoa(id))
		} else {
			warnings = append(warnings, fmt.Sprintf("recipe book %q not found; ignored as a filter", in.Book))
		}
	}
	addInt(q, "rating_gte", in.MinRating)
	addBool(q, "makenow", in.MakeableNow)
	addBool(q, "random", in.Random)
	if in.Newest != nil && *in.Newest {
		q.Set("sort_order", "-created_at")
	}
	limit := 25
	if in.Limit != nil && *in.Limit > 0 {
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
	out := findRecipesOutput{Count: env.Count, Returned: len(recipes), Warnings: warnings}
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
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Rating         string   `json:"rating,omitempty"`
	Servings       string   `json:"servings,omitempty"`
	WorkingTimeMin string   `json:"working_time_min,omitempty"`
	WaitingTimeMin string   `json:"waiting_time_min,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	Markdown       string   `json:"markdown"`
}

// getRecipe returns one recipe as structured fields plus a readable Markdown view.
func (h *handlers) getRecipe(ctx context.Context, _ *mcp.CallToolRequest, in getRecipeInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/", id), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, fmt.Errorf("decoding recipe: %w", err)
	}
	if in.Servings != nil && *in.Servings > 0 && r.Servings.Set && r.Servings.Value > 0 {
		scaleAmounts(&r, float64(*in.Servings)/r.Servings.Value)
		r.Servings = flexNum{Set: true, Value: float64(*in.Servings)}
	}
	return jsonResult(getRecipeOutput{
		ID: r.ID, Name: r.Name, Rating: r.Rating.String(), Servings: r.Servings.String(),
		WorkingTimeMin: r.WorkingTime.String(), WaitingTimeMin: r.WaitingTime.String(),
		Keywords: keywordNames(r.Keywords), SourceURL: r.SourceURL, Markdown: renderRecipe(r),
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
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Steps    int      `json:"steps"`
	Keywords []string `json:"keywords,omitempty"`
}

// createRecipe creates a recipe, splitting quantities out of natural-language
// ingredient lines and get-or-creating foods, units and keywords by name.
func (h *handlers) createRecipe(ctx context.Context, _ *mcp.CallToolRequest, in createRecipeInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, fmt.Errorf("name is required")
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
		return nil, nil, err
	}
	var created apiRecipe
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, nil, fmt.Errorf("decoding created recipe: %w", err)
	}
	return jsonResult(createRecipeOutput{
		ID: created.ID, Name: created.Name, Status: "created",
		Steps: len(created.Steps), Keywords: keywordNames(created.Keywords),
	})
}

// ---- import_recipe_from_url ----

type importRecipeInput struct {
	URL  string `json:"url" jsonschema:"recipe web page URL to import (http/https)"`
	Save *bool  `json:"save,omitempty" jsonschema:"save to your collection (default true); false returns a parsed preview only"`
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
	ID         int      `json:"id,omitempty"`
	Name       string   `json:"name,omitempty"`
	Status     string   `json:"status"`
	Source     string   `json:"source,omitempty"`
	ImageURL   string   `json:"image_url,omitempty"`
	Duplicates []named  `json:"duplicates,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Markdown   string   `json:"markdown,omitempty"`
}

// importRecipeFromURL scrapes a recipe and (by default) saves it.
func (h *handlers) importRecipeFromURL(ctx context.Context, _ *mcp.CallToolRequest, in importRecipeInput) (*mcp.CallToolResult, any, error) {
	if err := validateHTTPURL(in.URL); err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodPost, "recipe-from-source/", nil, map[string]any{"url": in.URL})
	if err != nil {
		if msg, ok := importErrorMessage(err); ok {
			return textResult("could not import: " + msg)
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
	// YouTube / Tandoor-share URLs are saved server-side and return only an id.
	if resp.RecipeID != nil && *resp.RecipeID > 0 {
		return jsonResult(importRecipeOutput{ID: *resp.RecipeID, Status: "imported", Source: in.URL, Duplicates: resp.Duplicates})
	}
	if resp.Error || len(resp.Recipe) == 0 || string(resp.Recipe) == "null" {
		msg := resp.Msg
		if msg == "" {
			msg = "no recipe found at that URL"
		}
		return textResult("could not import: " + msg)
	}
	var sr sourceRecipe
	if err := json.Unmarshal(resp.Recipe, &sr); err != nil {
		return nil, nil, fmt.Errorf("decoding parsed recipe: %w", err)
	}

	save := in.Save == nil || *in.Save
	if !save {
		return jsonResult(importRecipeOutput{
			Status: "preview", Name: sr.Name, Source: in.URL, ImageURL: sr.ImageURL,
			Duplicates: resp.Duplicates, Markdown: renderSourceRecipe(sr),
		})
	}

	body, warnings := sourceRecipeToBody(sr, in.URL)
	created, err := h.c.Do(ctx, http.MethodPost, "recipe/", nil, body)
	if err != nil {
		return nil, nil, err
	}
	var rec apiRecipe
	if err := json.Unmarshal(created, &rec); err != nil {
		return nil, nil, fmt.Errorf("decoding imported recipe: %w", err)
	}
	return jsonResult(importRecipeOutput{
		ID: rec.ID, Name: rec.Name, Status: "imported", Source: in.URL,
		ImageURL: sr.ImageURL, Duplicates: resp.Duplicates, Warnings: warnings,
	})
}

// ---- update_recipe ----

type updateRecipeInput struct {
	Recipe         string   `json:"recipe" jsonschema:"recipe name or numeric id"`
	Name           *string  `json:"name,omitempty" jsonschema:"new name"`
	Description    *string  `json:"description,omitempty" jsonschema:"new description"`
	Servings       *int     `json:"servings,omitempty" jsonschema:"new serving count"`
	WorkingTime    *int     `json:"working_time,omitempty" jsonschema:"prep time in minutes"`
	WaitingTime    *int     `json:"waiting_time,omitempty" jsonschema:"cooking/waiting time in minutes"`
	SourceURL      *string  `json:"source_url,omitempty" jsonschema:"new source URL"`
	AddKeywords    []string `json:"add_keywords,omitempty" jsonschema:"keyword names to add"`
	RemoveKeywords []string `json:"remove_keywords,omitempty" jsonschema:"keyword names to remove"`
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
		names, err := h.mergedKeywordNames(ctx, id, in.AddKeywords, in.RemoveKeywords)
		if err != nil {
			return nil, nil, err
		}
		body["keywords"] = keywordObjects(names)
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update: provide at least one field")
	}
	return h.patchRecipe(ctx, id, body, "updated")
}

// ---- set_recipe_steps ----

type setRecipeStepsInput struct {
	Recipe string      `json:"recipe" jsonschema:"recipe name or numeric id"`
	Steps  []stepInput `json:"steps" jsonschema:"the full new ordered list of steps and ingredients, replacing the existing ones"`
}

// setRecipeSteps replaces a recipe's steps/ingredients (re-describe to edit).
func (h *handlers) setRecipeSteps(ctx context.Context, _ *mcp.CallToolRequest, in setRecipeStepsInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
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

// mergedKeywordNames reads the recipe's current keywords and applies adds/removes.
func (h *handlers) mergedKeywordNames(ctx context.Context, recipeID int, add, remove []string) ([]string, error) {
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/", recipeID), nil, nil)
	if err != nil {
		return nil, err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decoding recipe: %w", err)
	}
	set := map[string]string{} // lower -> display
	for _, k := range r.Keywords {
		set[strings.ToLower(k.Name)] = k.Name
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
	return names, nil
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
	ImageURL  string `json:"image_url,omitempty" jsonschema:"remote image URL (http/https) for Tandoor to fetch and store"`
}

// setRecipeImage sets a recipe image from a (gated) local file or a remote URL.
func (h *handlers) setRecipeImage(ctx context.Context, _ *mcp.CallToolRequest, in recipeImageInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	path := fmt.Sprintf("recipe/%d/image/", id)
	switch {
	case in.ImagePath != "":
		full, err := h.safeImagePath(in.ImagePath)
		if err != nil {
			return nil, nil, err
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, nil, fmt.Errorf("opening image: %w", err)
		}
		defer f.Close()
		if _, err := h.c.Upload(ctx, http.MethodPut, path, nil, "image", filepath.Base(full), io.LimitReader(f, maxImageBytes)); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"status": "image set", "id": id, "from": "file"})
	case in.ImageURL != "":
		if err := validateHTTPURL(in.ImageURL); err != nil {
			return nil, nil, err
		}
		if _, err := h.c.Upload(ctx, http.MethodPut, path, map[string]string{"image_url": in.ImageURL}, "", "", nil); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"status": "image set", "id": id, "from": "url"})
	default:
		return nil, nil, fmt.Errorf("provide image_path or image_url")
	}
}

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
		return "", fmt.Errorf("opening image: %w", err)
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("image_path %q is outside the allowed image directory", p)
	}
	return full, nil
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
	return jsonResult(cards)
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

func keywordNames(ks []named) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.Name)
	}
	return out
}

func setInt(body map[string]any, key string, v *int) {
	if v != nil {
		body[key] = *v
	}
}

// validateHTTPURL rejects URLs that aren't http/https with a host, before any
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
	if u.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	return nil
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
