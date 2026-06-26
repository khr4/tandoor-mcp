package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// FindRecipes searches recipes using names (resolved to ids) and returns compact cards.
func (h *handlers) FindRecipes(ctx context.Context, _ *mcp.CallToolRequest, in findRecipesInput) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	addStr(q, "query", in.Text)

	var warnings []string
	if len(in.Keywords) > 0 {
		ids, missing, err := h.resolveExistingIDs(ctx, "keyword", in.Keywords)
		if err != nil {
			return nil, nil, err
		}
		addInts(q, "keywords", ids)
		warnings = appendMissing(warnings, "keyword", missing)
	}
	if len(in.Ingredients) > 0 {
		ids, missing, err := h.resolveExistingIDs(ctx, "food", in.Ingredients)
		if err != nil {
			return nil, nil, err
		}
		addInts(q, "foods", ids)
		warnings = appendMissing(warnings, "ingredient", missing)
	}
	if in.Book != "" {
		id, found, err := h.resolveExistingID(ctx, "recipe-book", in.Book)
		if err != nil {
			return nil, nil, err
		}
		if found {
			q.Set("books", fmt.Sprintf("%d", id))
		} else {
			warnings = append(warnings, fmt.Sprintf("recipe book %q not found", in.Book))
		}
	}
	addInt(q, "rating_gte", in.MinRating)
	addBool(q, "makenow", in.MakeableNow)
	addBool(q, "new", in.Newest)
	addBool(q, "random", in.Random)
	if in.Newest != nil && *in.Newest {
		q.Set("sort_order", "-created_at")
	}
	limit := 25
	if in.Limit != nil && *in.Limit > 0 {
		limit = *in.Limit
	}
	q.Set("page_size", fmt.Sprintf("%d", limit))

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

type recipeIDInput struct {
	ID int `json:"id" jsonschema:"recipe id"`
}

// GetRecipe returns one recipe rendered as readable Markdown.
func (h *handlers) GetRecipe(ctx context.Context, _ *mcp.CallToolRequest, in recipeIDInput) (*mcp.CallToolResult, any, error) {
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/", in.ID), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, fmt.Errorf("decoding recipe: %w", err)
	}
	return textResult(renderRecipe(r))
}

// ---- create_recipe ----

type createRecipeInput struct {
	Name        string      `json:"name" jsonschema:"recipe name"`
	Description string      `json:"description,omitempty"`
	Servings    *int        `json:"servings,omitempty" jsonschema:"number of servings the ingredient amounts make"`
	WorkingTime *int        `json:"working_time,omitempty" jsonschema:"active/prep time in minutes"`
	WaitingTime *int        `json:"waiting_time,omitempty" jsonschema:"waiting/cooking time in minutes"`
	SourceURL   string      `json:"source_url,omitempty"`
	Keywords    []string    `json:"keywords,omitempty" jsonschema:"tag names; created if they do not exist"`
	Steps       []stepInput `json:"steps,omitempty" jsonschema:"ordered preparation steps and their ingredients"`
}

type createRecipeOutput struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Steps    int      `json:"steps"`
	Keywords []string `json:"keywords,omitempty"`
}

// CreateRecipe creates a recipe, splitting quantities out of natural-language
// ingredient lines and get-or-creating foods, units and keywords by name.
func (h *handlers) CreateRecipe(ctx context.Context, _ *mcp.CallToolRequest, in createRecipeInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	steps, err := h.buildSteps(ctx, in.Steps)
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
	URL  string `json:"url" jsonschema:"recipe web page URL to import"`
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
	ID             int     `json:"id,omitempty"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	Source         string  `json:"source"`
	ImageAvailable bool    `json:"image_available"`
	Duplicates     []named `json:"duplicates,omitempty"`
	Note           string  `json:"note,omitempty"`
}

// ImportRecipeFromURL scrapes a recipe and (by default) saves it.
func (h *handlers) ImportRecipeFromURL(ctx context.Context, _ *mcp.CallToolRequest, in importRecipeInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.URL) == "" {
		return nil, nil, fmt.Errorf("url is required")
	}
	raw, err := h.c.Do(ctx, http.MethodPost, "recipe-from-source/", nil, map[string]any{"url": in.URL})
	if err != nil {
		return nil, nil, err
	}
	var resp struct {
		Recipe     json.RawMessage `json:"recipe"`
		Error      bool            `json:"error"`
		Msg        string          `json:"msg"`
		Duplicates []named         `json:"duplicates"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("decoding scrape response: %w", err)
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

	save := true
	if in.Save != nil {
		save = *in.Save
	}
	if !save {
		preview := "PREVIEW (not saved)\n\n" + renderSourceRecipe(sr)
		if len(resp.Duplicates) > 0 {
			preview += "\nPossible duplicates already in your collection: " + namesJoin(resp.Duplicates)
		}
		return textResult(preview)
	}

	body := sourceRecipeToBody(sr, in.URL)
	created, err := h.c.Do(ctx, http.MethodPost, "recipe/", nil, body)
	if err != nil {
		return nil, nil, err
	}
	var rec apiRecipe
	if err := json.Unmarshal(created, &rec); err != nil {
		return nil, nil, fmt.Errorf("decoding imported recipe: %w", err)
	}
	out := importRecipeOutput{
		ID: rec.ID, Name: rec.Name, Status: "imported", Source: in.URL,
		ImageAvailable: sr.ImageURL != "", Duplicates: resp.Duplicates,
	}
	if sr.ImageURL != "" {
		out.Note = "the source has an image; call set_recipe_image with image_url to attach it"
	}
	return jsonResult(out)
}

// ---- update_recipe ----

type updateRecipeInput struct {
	ID             int      `json:"id" jsonschema:"recipe id"`
	Name           *string  `json:"name,omitempty"`
	Description    *string  `json:"description,omitempty"`
	Servings       *int     `json:"servings,omitempty"`
	WorkingTime    *int     `json:"working_time,omitempty" jsonschema:"prep time in minutes"`
	WaitingTime    *int     `json:"waiting_time,omitempty" jsonschema:"cooking/waiting time in minutes"`
	SourceURL      *string  `json:"source_url,omitempty"`
	AddKeywords    []string `json:"add_keywords,omitempty" jsonschema:"keyword names to add"`
	RemoveKeywords []string `json:"remove_keywords,omitempty" jsonschema:"keyword names to remove"`
}

// UpdateRecipe applies targeted edits without resending the whole recipe.
func (h *handlers) UpdateRecipe(ctx context.Context, _ *mcp.CallToolRequest, in updateRecipeInput) (*mcp.CallToolResult, any, error) {
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
		names, err := h.mergedKeywordNames(ctx, in.ID, in.AddKeywords, in.RemoveKeywords)
		if err != nil {
			return nil, nil, err
		}
		body["keywords"] = keywordObjects(names)
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("nothing to update: provide at least one field")
	}

	raw, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("recipe/%d/", in.ID), nil, body)
	if err != nil {
		return nil, nil, err
	}
	var r apiRecipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, fmt.Errorf("decoding updated recipe: %w", err)
	}
	return jsonResult(map[string]any{"status": "updated", "recipe": toCard(r)})
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
		n = strings.TrimSpace(n)
		if n != "" {
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

// ---- set_recipe_image ----

type recipeImageInput struct {
	ID        int    `json:"id" jsonschema:"recipe id"`
	ImagePath string `json:"image_path,omitempty" jsonschema:"local filesystem path to an image file to upload"`
	ImageURL  string `json:"image_url,omitempty" jsonschema:"remote image URL for Tandoor to fetch and store"`
}

// SetRecipeImage sets a recipe image from a local file or a remote URL.
func (h *handlers) SetRecipeImage(ctx context.Context, _ *mcp.CallToolRequest, in recipeImageInput) (*mcp.CallToolResult, any, error) {
	path := fmt.Sprintf("recipe/%d/image/", in.ID)
	switch {
	case in.ImagePath != "":
		f, err := os.Open(in.ImagePath)
		if err != nil {
			return nil, nil, fmt.Errorf("opening image: %w", err)
		}
		defer f.Close()
		if _, err := h.c.Upload(ctx, http.MethodPut, path, nil, "image", filepath.Base(in.ImagePath), f); err != nil {
			return nil, nil, err
		}
		return textResult(fmt.Sprintf("image set on recipe %d from %s", in.ID, filepath.Base(in.ImagePath)))
	case in.ImageURL != "":
		if _, err := h.c.Upload(ctx, http.MethodPut, path, map[string]string{"image_url": in.ImageURL}, "", "", nil); err != nil {
			return nil, nil, err
		}
		return textResult(fmt.Sprintf("image set on recipe %d from URL", in.ID))
	default:
		return nil, nil, fmt.Errorf("provide image_path or image_url")
	}
}

// ---- find_related_recipes ----

// FindRelatedRecipes lists recipes related to the given recipe.
func (h *handlers) FindRelatedRecipes(ctx context.Context, _ *mcp.CallToolRequest, in recipeIDInput) (*mcp.CallToolResult, any, error) {
	raw, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("recipe/%d/related/", in.ID), nil, nil)
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
	RecipeID int    `json:"recipe_id" jsonschema:"id of the recipe that was cooked"`
	Rating   *int   `json:"rating,omitempty" jsonschema:"rating to record, 0-5"`
	Servings *int   `json:"servings,omitempty" jsonschema:"servings made"`
	Comment  string `json:"comment,omitempty"`
}

// LogCooked records a cook-log entry (which is also how a recipe gets rated).
func (h *handlers) LogCooked(ctx context.Context, _ *mcp.CallToolRequest, in logCookedInput) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"recipe": in.RecipeID}
	setInt(body, "rating", in.Rating)
	setInt(body, "servings", in.Servings)
	if in.Comment != "" {
		body["comment"] = in.Comment
	}
	if _, err := h.c.Do(ctx, http.MethodPost, "cook-log/", nil, body); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("logged a cook of recipe %d", in.RecipeID))
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

func appendMissing(warnings []string, kind string, missing []string) []string {
	for _, m := range missing {
		warnings = append(warnings, fmt.Sprintf("%s %q not found; it was ignored as a filter", kind, m))
	}
	return warnings
}

func namesJoin(ns []named) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, fmt.Sprintf("%s (id %d)", n.Name, n.ID))
	}
	return strings.Join(parts, ", ")
}

// sourceRecipeToBody maps a scraped recipe into a recipe-create payload.
func sourceRecipeToBody(sr sourceRecipe, sourceURL string) map[string]any {
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
				continue // skip ingredients the scraper could not attribute to a food
			}
			if payload, err := buildIngredient(in, nil); err == nil && payload != nil {
				if ing.OriginalText != "" {
					payload["original_text"] = ing.OriginalText
				}
				ings = append(ings, payload)
			}
		}
		steps = append(steps, map[string]any{
			"instruction":            st.Instruction,
			"ingredients":            ings,
			"show_ingredients_table": true,
			"order":                  si,
		})
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
	return body
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
