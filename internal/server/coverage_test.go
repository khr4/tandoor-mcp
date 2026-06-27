package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- resolve helpers ----

func TestResolveExistingIDFindsMatchOnLaterPage(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "page=1") {
			// Fuzzy matches first; the exact "salt" is only on page 2.
			_, _ = io.WriteString(w, `{"next":"x","results":[{"id":1,"name":"salted butter"},{"id":2,"name":"salt flakes"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":9,"name":"Salt"}]}`)
	})
	id, found, err := h.resolveExistingID(context.Background(), "food", "salt")
	if err != nil || !found || id != 9 {
		t.Fatalf("resolveExistingID = (%d,%v,%v), want (9,true,nil)", id, found, err)
	}
}

func TestResolveRecipeNumericAndName(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":12,"name":"Stew"}]}`)
	})
	if id, err := h.resolveRecipe(context.Background(), "42"); err != nil || id != 42 {
		t.Errorf("numeric resolveRecipe = (%d,%v)", id, err)
	}
	if id, err := h.resolveRecipe(context.Background(), "Stew"); err != nil || id != 12 {
		t.Errorf("named resolveRecipe = (%d,%v)", id, err)
	}
}

func TestResolveRecipeNotFound(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
	})
	if _, err := h.resolveRecipe(context.Background(), "Ghost"); err == nil {
		t.Error("expected not-found error")
	}
}

func TestGetOrCreateIDPostsName(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST (server-side get-or-create)", r.Method)
		}
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":7,"name":"Salt"}`)
	})
	id, err := h.getOrCreateID(context.Background(), "food", "Salt")
	if err != nil || id != 7 {
		t.Fatalf("getOrCreateID = (%d,%v), want (7,nil)", id, err)
	}
	if at(t, body, "name") != "Salt" {
		t.Errorf("body = %v", body)
	}
}

// ---- recipes ----

func TestGetRecipeScalesAmounts(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe/7/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":7,"name":"Soup","servings":2,"keywords":[{"name":"easy"}],"steps":[{"instruction":"boil","ingredients":[{"amount":2,"unit":{"name":"cup"},"food":{"name":"flour"}}]}]}`)
	})
	four := 4
	res, _, err := h.getRecipe(context.Background(), nil, getRecipeInput{Recipe: "7", Servings: &four})
	if err != nil {
		t.Fatalf("getRecipe: %v", err)
	}
	var out getRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != 7 || out.Servings != "4" {
		t.Errorf("out = %+v, want id 7 servings 4", out)
	}
	if len(out.Steps) != 1 || len(out.Steps[0].Ingredients) != 1 || out.Steps[0].Ingredients[0].Amount == nil {
		t.Errorf("structured steps = %+v, want stored amount 2 for safe editing", out.Steps)
	} else if got := *out.Steps[0].Ingredients[0].Amount; got != 2 {
		t.Errorf("structured step amount = %v, want stored amount 2 for safe editing", got)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("expected scaling warning")
	}
	if !strings.Contains(out.Markdown, "4 cup flour") {
		t.Errorf("markdown not scaled: %q", out.Markdown)
	}
}

func TestUpdateRecipeMergesKeywords(t *testing.T) {
	var patchBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":5,"name":"Chili","keywords":[{"name":"spicy"}]}`)
		case http.MethodPatch:
			patchBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":5,"name":"Chili","keywords":[{"name":"spicy"},{"name":"vegan"}]}`)
		}
	})
	res, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{Recipe: "5", AddKeywords: []string{"vegan"}, RemoveKeywords: []string{"spicy"}})
	if err != nil {
		t.Fatalf("updateRecipe: %v", err)
	}
	kws := at(t, patchBody, "keywords")
	names := map[string]bool{}
	for _, k := range kws.([]any) {
		names[at(t, k, "name").(string)] = true
	}
	if !names["vegan"] || names["spicy"] {
		t.Errorf("merged keywords = %v, want vegan without spicy", names)
	}
	if !strings.Contains(resultText(t, res), `"status": "updated"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestFindRecipesBuildsAllHighValueFilters(t *testing.T) {
	var recipeQuery string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/food/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":9,"name":"Tomato"}]}`)
		case "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":4,"name":"Dinner"}]}`)
		case "/api/recipe/":
			recipeQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"count":0,"results":[]}`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	})
	minRating := 4
	makeable := true
	newest := true
	random := true
	limit := 7
	if _, _, err := h.findRecipes(context.Background(), nil, findRecipesInput{
		Ingredients: []string{"Tomato"},
		Book:        "Dinner",
		MinRating:   &minRating,
		MakeableNow: &makeable,
		Newest:      &newest,
		Random:      &random,
		Limit:       &limit,
	}); err != nil {
		t.Fatalf("findRecipes: %v", err)
	}
	for _, want := range []string{"foods_and=9", "books_and=4", "rating_gte=4", "makenow=true", "sort_order=-created_at", "random=true", "page_size=7"} {
		if !strings.Contains(recipeQuery, want) {
			t.Errorf("query %q missing %q", recipeQuery, want)
		}
	}
}

func TestUpdateRecipeRequiresAField(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {})
	if _, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{Recipe: "5"}); err == nil {
		t.Error("expected error when no fields given")
	}
}

func TestSetRecipeStepsPatchesSteps(t *testing.T) {
	var patchBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ingredient-parser/post/":
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":1,"unit":{"name":"tsp"},"food":{"name":"salt"}}]}`)
		case r.Method == http.MethodPatch:
			patchBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":5,"name":"X"}`)
		}
	})
	_, _, err := h.setRecipeSteps(context.Background(), nil, setRecipeStepsInput{
		Recipe: "5",
		Steps:  []stepInput{{Instruction: "season", Ingredients: []ingredientInput{{Text: "1 tsp salt"}}}},
	})
	if err != nil {
		t.Fatalf("setRecipeSteps: %v", err)
	}
	if at(t, patchBody, "steps") == nil {
		t.Errorf("patch body missing steps: %v", patchBody)
	}
}

func TestDeleteRecipe(t *testing.T) {
	var method, path string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	res, _, err := h.deleteRecipe(context.Background(), nil, deleteRecipeInput{Recipe: "5"})
	if err != nil {
		t.Fatalf("deleteRecipe: %v", err)
	}
	if method != http.MethodDelete || path != "/api/recipe/5/" {
		t.Errorf("got %s %s", method, path)
	}
	if !strings.Contains(resultText(t, res), `"status": "deleted"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestFindRelatedRecipes(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe/5/related/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"id":8,"name":"Twin Stew","rating":"3"}]`)
	})
	res, _, err := h.findRelatedRecipes(context.Background(), nil, recipeRefInput{Recipe: "5"})
	if err != nil {
		t.Fatalf("findRelatedRecipes: %v", err)
	}
	if !strings.Contains(resultText(t, res), "Twin Stew") {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestLogCookedPostsRating(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cook-log/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":1}`)
	})
	rating := 5
	_, _, err := h.logCooked(context.Background(), nil, logCookedInput{Recipe: "5", Rating: &rating})
	if err != nil {
		t.Fatalf("logCooked: %v", err)
	}
	if at(t, body, "recipe") != 5.0 || at(t, body, "rating") != 5.0 {
		t.Errorf("body = %v, want recipe 5 rating 5 (bare ids)", body)
	}
}

func TestImportFriendlyErrorOn400(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":true,"msg":"No usable data could be found."}`)
	})
	if _, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/none"}); err == nil || !strings.Contains(err.Error(), "No usable data could be found.") {
		t.Fatalf("importRecipeFromURL err = %v, want friendly import error", err)
	}
}

func TestImportYouTubeReturnsRecipeID(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/recipe/" {
			t.Error("youtube import must not re-POST a recipe body")
		}
		_, _ = io.WriteString(w, `{"recipe":null,"recipe_id":77,"images":[],"duplicates":[]}`)
	})
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://youtu.be/abc"})
	if err != nil {
		t.Fatalf("importRecipeFromURL: %v", err)
	}
	var out importRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != 77 || out.Status != "imported" {
		t.Errorf("out = %+v, want id 77 imported", out)
	}
}

func TestImportPreviewErrorsWhenServerAlreadySaved(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"recipe":null,"recipe_id":77,"images":[],"duplicates":[]}`)
	})
	save := false
	if _, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://youtu.be/abc", Save: &save}); err == nil || !strings.Contains(err.Error(), "already imported") {
		t.Fatalf("importRecipeFromURL err = %v, want already-imported preview error", err)
	}
}

func TestImportRejectsNonHTTPURL(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("backend must not be called for a file:// URL")
	})
	if _, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "file:///etc/passwd"}); err == nil {
		t.Error("expected scheme rejection")
	}
}

func TestImportRefusesDroppedIngredientByDefault(t *testing.T) {
	scrape := `{"recipe":{"name":"Soup","steps":[{"instruction":"x","ingredients":[{"amount":1,"unit":{"name":"l"},"original_text":"1 l of mystery"}]}]},"images":[],"duplicates":[]}`
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/recipe-from-source/":
			_, _ = io.WriteString(w, scrape)
		case "/api/recipe/":
			t.Fatal("lossy import must not save by default")
		}
	})
	if _, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup"}); err == nil || !strings.Contains(err.Error(), "would be dropped") {
		t.Fatalf("importRecipeFromURL err = %v, want dropped-ingredient refusal", err)
	}
}

func TestImportAllowsDroppedIngredientWhenExplicit(t *testing.T) {
	scrape := `{"recipe":{"name":"Soup","steps":[{"instruction":"x","ingredients":[{"amount":1,"unit":{"name":"l"},"original_text":"1 l of mystery"}]}]},"images":[],"duplicates":[]}`
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/recipe-from-source/":
			_, _ = io.WriteString(w, scrape)
		case "/api/recipe/":
			_, _ = io.WriteString(w, `{"id":3,"name":"Soup"}`)
		}
	})
	allow := true
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup", AllowPartial: &allow})
	if err != nil {
		t.Fatalf("importRecipeFromURL: %v", err)
	}
	var out importRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "mystery") {
		t.Errorf("warnings = %v, want one about the dropped ingredient", out.Warnings)
	}
}

// ---- set_recipe_image gating ----

func TestSetRecipeImageURL(t *testing.T) {
	var path string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	})
	_, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImageURL: "https://img.example/x.png"})
	if err != nil {
		t.Fatalf("setRecipeImage: %v", err)
	}
	if path != "/api/recipe/5/image/" {
		t.Errorf("path = %s", path)
	}
}

func TestSetRecipeImageRejectsBadScheme(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {})
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImageURL: "ftp://x/y"}); err == nil {
		t.Error("expected scheme rejection")
	}
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImageURL: "http://127.0.0.1/x.png"}); err == nil {
		t.Error("expected loopback SSRF rejection")
	}
}

func TestSetRecipeImagePathGating(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "pie.png")
	if err := os.WriteFile(inside, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("X"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })

	// imageDir unset → local path refused.
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImagePath: inside}); err == nil {
		t.Error("expected refusal when image dir is not configured")
	}

	h.imageDir = dir
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImagePath: inside}); err != nil {
		t.Errorf("path inside allowed dir should succeed: %v", err)
	}
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImagePath: outside}); err == nil {
		t.Error("path outside allowed dir must be rejected")
	}
}

// ---- planning ----

func TestGetMealPlanUsesDateFilterAndEndDate(t *testing.T) {
	var query string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `[{"id":3,"from_date":"2026-07-01T00:00:00","to_date":"2026-07-03T00:00:00","meal_type":{"name":"Dinner"},"recipe":{"id":5,"name":"Stew"},"note":"prep ahead"}]`)
	})
	res, _, err := h.getMealPlan(context.Background(), nil, getMealPlanInput{From: "2026-07-01", To: "2026-07-31"})
	if err != nil {
		t.Fatalf("getMealPlan: %v", err)
	}
	if !strings.Contains(query, "from_date=2026-07-01") || strings.Contains(query, "T00") {
		t.Errorf("query = %q, want plain dates", query)
	}
	var cards []mealPlanCard
	if err := json.Unmarshal([]byte(resultText(t, res)), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].EndDate == "" || cards[0].Recipe != "Stew" || cards[0].Note != "prep ahead" {
		t.Errorf("cards = %+v", cards)
	}
}

func TestRemoveMealPlanEntry(t *testing.T) {
	var method, path string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if _, _, err := h.removeMealPlanEntry(context.Background(), nil, removeMealPlanInput{ID: 3}); err != nil {
		t.Fatalf("removeMealPlanEntry: %v", err)
	}
	if method != http.MethodDelete || path != "/api/meal-plan/3/" {
		t.Errorf("got %s %s", method, path)
	}
}

// ---- shopping ----

func TestAddRecipeToShopping(t *testing.T) {
	var path string
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{}`)
	})
	three := 3
	_, _, err := h.addRecipeToShopping(context.Background(), nil, addRecipeShoppingInput{Recipe: "5", Servings: &three})
	if err != nil {
		t.Fatalf("addRecipeToShopping: %v", err)
	}
	if path != "/api/recipe/5/shopping/" || at(t, body, "servings") != 3.0 {
		t.Errorf("got %s body %v", path, body)
	}
}

func TestUpdateShoppingItem(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{}`)
	})
	checked := true
	if _, _, err := h.updateShoppingItem(context.Background(), nil, updateShoppingInput{ID: 3, Checked: &checked}); err != nil {
		t.Fatalf("updateShoppingItem: %v", err)
	}
	if at(t, body, "checked") != true {
		t.Errorf("body = %v", body)
	}
	if _, _, err := h.updateShoppingItem(context.Background(), nil, updateShoppingInput{ID: 3}); err == nil {
		t.Error("expected error with no fields")
	}
}

func TestClearShoppingListAggregatesErrors(t *testing.T) {
	deleted := map[string]bool{}
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":1,"checked":true},{"id":2,"checked":true},{"id":3,"checked":false}]}`)
			return
		}
		// DELETE
		if r.URL.Path == "/api/shopping-list-entry/2/" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `boom`)
			return
		}
		deleted[r.URL.Path] = true
		w.WriteHeader(http.StatusNoContent)
	})
	res, _, err := h.clearShoppingList(context.Background(), nil, clearShoppingInput{})
	if err != nil {
		t.Fatalf("clearShoppingList: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	// Only checked items 1 and 2 are attempted; 2 fails, so removed=1, status partial.
	if out["removed"] != 1.0 || out["status"] != "partial" {
		t.Errorf("out = %v, want removed 1 partial", out)
	}
	if !deleted["/api/shopping-list-entry/1/"] {
		t.Error("entry 1 should have been deleted despite entry 2 failing")
	}
}

// ---- pantry / taxonomy ----

func TestGetPantryFiltersOnHand(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":1,"name":"Milk","food_onhand":true},{"id":2,"name":"Flour","food_onhand":false}]}`)
	})
	res, _, err := h.getPantry(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("getPantry: %v", err)
	}
	var out pantryOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.OnHand) != 1 || out.OnHand[0].Name != "Milk" {
		t.Errorf("on hand = %+v, want only Milk", out.OnHand)
	}
}

func TestListTaxonomyAndBadKind(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/unit/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"id":1,"name":"gram"}]`)
	})
	res, _, err := h.listTaxonomy(context.Background(), nil, listTaxonomyInput{Kind: "unit"})
	if err != nil {
		t.Fatalf("listTaxonomy: %v", err)
	}
	if !strings.Contains(resultText(t, res), "gram") {
		t.Errorf("result = %s", resultText(t, res))
	}
	if _, _, err := h.listTaxonomy(context.Background(), nil, listTaxonomyInput{Kind: "bogus"}); err == nil {
		t.Error("expected error for bad kind")
	}
}

func TestCreateRecipeWrapsTopLevelIngredients(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ingredient-parser/post/":
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":1,"unit":{"name":"tsp"},"food":{"name":"salt"}}]}`)
		case "/api/recipe/":
			body = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":1,"name":"Quick","steps":[{}]}`)
		}
	})
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name:         "Quick",
		Instructions: "mix it",
		Ingredients:  []ingredientInput{{Text: "1 tsp salt"}},
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	step := idx(t, at(t, body, "steps"), 0)
	if at(t, step, "instruction") != "mix it" {
		t.Errorf("instruction = %v", at(t, step, "instruction"))
	}
	if ings := at(t, step, "ingredients").([]any); len(ings) != 1 {
		t.Errorf("ingredients = %v, want one wrapped into a single step", ings)
	}
}

func TestImportPreviewRendersSteps(t *testing.T) {
	scrape := `{"recipe":{"name":"Soup","steps":[{"instruction":"boil","ingredients":[{"amount":1,"food":{"name":"water"},"unit":{"name":"l"},"original_text":"1 l water"}]}]},"images":[],"duplicates":[]}`
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/recipe/" {
			t.Error("preview must not save")
		}
		_, _ = io.WriteString(w, scrape)
	})
	save := false
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup", Save: &save})
	if err != nil {
		t.Fatalf("importRecipeFromURL: %v", err)
	}
	var out importRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "preview" || !strings.Contains(out.Markdown, "1 l water") {
		t.Errorf("preview markdown = %q", out.Markdown)
	}
}

func TestBuildIngredientHeader(t *testing.T) {
	m, err := buildIngredient(ingredientInput{IsHeader: true, Note: "For the sauce"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m["is_header"] != true || m["note"] != "For the sauce" {
		t.Errorf("header ingredient = %v", m)
	}
}

// TestGetRecipeStepsRoundTripToSetSteps proves get_recipe's structured steps
// deserialize directly into the set_recipe_steps input and rebuild losslessly,
// including a no-amount ingredient and a section header — no Markdown parsing.
func TestGetRecipeStepsRoundTripToSetSteps(t *testing.T) {
	var r apiRecipe
	if err := json.Unmarshal([]byte(`{"id":1,"name":"X","steps":[{"instruction":"mix","time":5,"ingredients":[
		{"amount":2,"unit":{"name":"cup"},"food":{"name":"flour"}},
		{"food":{"name":"salt"},"no_amount":true,"note":"to taste"},
		{"is_header":true,"note":"For the topping"}]}]}`), &r); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(toStepOuts(r))
	var in []stepInput
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("round-trip decode into set_recipe_steps input: %v", err)
	}
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no parser call expected for already-structured ingredients")
	})
	built, err := h.buildSteps(context.Background(), in)
	if err != nil {
		t.Fatalf("buildSteps: %v", err)
	}
	if len(built) != 1 || built[0]["time"] != 5 {
		t.Fatalf("step = %v", built)
	}
	ings := built[0]["ingredients"].([]map[string]any)
	if len(ings) != 3 {
		t.Fatalf("ingredients = %d, want 3", len(ings))
	}
	if ings[0]["amount"] != 2.0 || at(t, ings[0], "food", "name") != "flour" {
		t.Errorf("ing0 = %v", ings[0])
	}
	if ings[1]["no_amount"] != true || at(t, ings[1], "food", "name") != "salt" {
		t.Errorf("ing1 = %v", ings[1])
	}
	if ings[2]["is_header"] != true || ings[2]["note"] != "For the topping" {
		t.Errorf("ing2 header = %v", ings[2])
	}
}

func TestCardKeywordFallsBackToLabel(t *testing.T) {
	// The recipe LIST endpoint returns keywords as {id,label} with no name.
	var r apiRecipe
	if err := json.Unmarshal([]byte(`{"id":2,"name":"Live Pancakes","keywords":[{"id":2,"label":"breakfast"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	card := toCard(r)
	if len(card.Keywords) != 1 || card.Keywords[0] != "breakfast" {
		t.Errorf("card keywords = %v, want [breakfast] from label fallback", card.Keywords)
	}
}

// ---- v0.2.1 hardening ----

func TestResolveRecipeAmbiguousNameErrors(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Soup"},{"id":9,"name":"Soup"}]}`)
	})
	if _, err := h.resolveRecipe(context.Background(), "Soup"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v, want ambiguity error", err)
	}
}

func TestBuildStepsRejectsParserCountMismatch(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ingredient-parser/post/" {
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":1,"food":{"name":"salt"}}]}`) // 1 result for 2 lines
			return
		}
		t.Error("recipe must not be posted on a parser count mismatch")
	})
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name:        "X",
		Ingredients: []ingredientInput{{Text: "1 tsp salt"}, {Text: "2 cups flour"}},
	})
	if err == nil || !strings.Contains(err.Error(), "parser returned") {
		t.Errorf("err = %v, want count-mismatch error", err)
	}
}

func TestGetOrCreateIDFallsBackOnDuplicate(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"name":["already exists"]}`)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":5,"name":"Salt"}]}`)
		}
	})
	id, err := h.getOrCreateID(context.Background(), "food", "Salt")
	if err != nil || id != 5 {
		t.Fatalf("getOrCreateID = (%d,%v), want (5,nil) via fallback", id, err)
	}
}

func TestFlexNumKeepsRawAndBuildIngredientNoSilentZero(t *testing.T) {
	var s struct {
		A flexNum `json:"a"`
	}
	if err := json.Unmarshal([]byte(`{"a":"a pinch"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.A.Set || s.A.String() != "a pinch" {
		t.Errorf("flexNum = %+v, want raw 'a pinch' (not zeroed)", s.A)
	}
	p := apiIngredient{Amount: flexNum{Raw: "a pinch"}, Food: &apiFood{Name: "salt"}}
	m, err := buildIngredient(ingredientInput{Text: "a pinch of salt"}, &p)
	if err != nil {
		t.Fatal(err)
	}
	if m["no_amount"] != true {
		t.Errorf("no_amount = %v, want true for a non-numeric amount", m["no_amount"])
	}
}

func TestBuildIngredientUnitWithoutAmount(t *testing.T) {
	m, err := buildIngredient(ingredientInput{Unit: "pinch", Food: "salt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m["no_amount"] != true {
		t.Errorf("no_amount = %v, want true (unit present but no amount)", m["no_amount"])
	}
}

func TestShoppingEntriesPaginates(t *testing.T) {
	var reqs int
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if strings.Contains(r.URL.RawQuery, "page=1") {
			_, _ = io.WriteString(w, `{"next":"x","results":[{"id":1,"checked":false}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":2,"checked":false}]}`)
	})
	entries, truncated, err := h.shoppingEntries(context.Background())
	if err != nil {
		t.Fatalf("shoppingEntries: %v", err)
	}
	if len(entries) != 2 || reqs != 2 || truncated {
		t.Errorf("entries=%d reqs=%d truncated=%v, want 2, 2, false", len(entries), reqs, truncated)
	}
}

func TestShoppingEntriesStopsAtPageCap(t *testing.T) {
	var reqs int
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		reqs++
		_, _ = io.WriteString(w, `{"next":"always","results":[{"id":1}]}`) // next never clears
	})
	if _, truncated, err := h.shoppingEntries(context.Background()); err != nil {
		t.Fatalf("shoppingEntries: %v", err)
	} else if !truncated {
		t.Fatal("expected truncation after page cap")
	}
	if reqs != shoppingScanPages {
		t.Errorf("reqs = %d, want capped at %d", reqs, shoppingScanPages)
	}
}

func TestClearShoppingListRefusesTruncatedScan(t *testing.T) {
	deleted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			t.Fatal("clear must not delete after a truncated scan")
		}
		_, _ = io.WriteString(w, `{"next":"always","results":[{"id":1,"checked":true}]}`)
	})
	if _, _, err := h.clearShoppingList(context.Background(), nil, clearShoppingInput{}); err == nil {
		t.Fatal("expected truncated clear to fail")
	}
	if deleted {
		t.Fatal("delete was attempted after truncated scan")
	}
}

func TestSafeImagePathUniformErrorNoOracle(t *testing.T) {
	dir := t.TempDir()
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {})
	h.imageDir = dir

	_, errMissing := h.safeImagePath(filepath.Join(dir, "nope.png"))
	outside := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOutside := h.safeImagePath(outside)

	if errMissing == nil || errOutside == nil {
		t.Fatal("both an absent and an outside path must error")
	}
	if errMissing.Error() != errOutside.Error() {
		t.Errorf("error strings differ (existence oracle): %q vs %q", errMissing, errOutside)
	}
	for _, leak := range []string{"no such file", "permission denied", dir} {
		if strings.Contains(errMissing.Error(), leak) {
			t.Errorf("error leaks %q: %s", leak, errMissing)
		}
	}
}

func TestSetRecipeImageRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageBytes + 1); err != nil { // sparse file, no real bytes written
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	posted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		posted = true
		_, _ = io.WriteString(w, `{}`)
	})
	h.imageDir = dir
	if _, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{Recipe: "5", ImagePath: big}); err == nil {
		t.Error("expected oversize rejection")
	}
	if posted {
		t.Error("oversize image must not be uploaded")
	}
}

func TestMergeTaxonomyResolvesNames(t *testing.T) {
	var path string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// resolve names to ids
			if strings.Contains(r.URL.RawQuery, "Old") {
				_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Old"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"next":null,"results":[{"id":4,"name":"New"}]}`)
			}
			return
		}
		path = r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	})
	_, _, err := h.mergeTaxonomy(context.Background(), nil, mergeTaxonomyInput{Kind: "keyword", Source: "Old", Target: "New"})
	if err != nil {
		t.Fatalf("mergeTaxonomy: %v", err)
	}
	if path != "/api/keyword/3/merge/4/" {
		t.Errorf("path = %s, want /api/keyword/3/merge/4/", path)
	}
}

func TestMergeTaxonomyRejectsAmbiguousName(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Old"},{"id":4,"name":"old"}]}`)
	})
	if _, _, err := h.mergeTaxonomy(context.Background(), nil, mergeTaxonomyInput{Kind: "keyword", Source: "Old", Target: "Target"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("mergeTaxonomy err = %v, want ambiguity", err)
	}
}

// ---- get_recipe nutrition ----

func TestGetRecipeSurfacesNutrition(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe/7/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":7,"name":"Soup","servings":2,
			"nutrition":{"calories":"250.00","carbohydrates":"30","fats":"8","proteins":"12","source":"label"},
			"properties":[{"property_amount":"3.5","property_type":{"name":"Fiber","unit":"g"}}],
			"steps":[]}`)
	})
	res, _, err := h.getRecipe(context.Background(), nil, getRecipeInput{Recipe: "7"})
	if err != nil {
		t.Fatalf("getRecipe: %v", err)
	}
	var out getRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Nutrition == nil {
		t.Fatalf("nutrition missing: %s", resultText(t, res))
	}
	if out.Nutrition.Calories != "250" || out.Nutrition.Carbohydrates != "30" || out.Nutrition.Source != "label" {
		t.Errorf("nutrition = %+v", out.Nutrition)
	}
	if len(out.Properties) != 1 || out.Properties[0].Name != "Fiber" || out.Properties[0].Amount != "3.5" || out.Properties[0].Unit != "g" {
		t.Errorf("properties = %+v", out.Properties)
	}
}

func TestGetRecipeOmitsAbsentNutrition(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":8,"name":"Plain","servings":1,"steps":[]}`)
	})
	res, _, err := h.getRecipe(context.Background(), nil, getRecipeInput{Recipe: "8"})
	if err != nil {
		t.Fatalf("getRecipe: %v", err)
	}
	var out getRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Nutrition != nil {
		t.Errorf("nutrition = %+v, want nil when absent", out.Nutrition)
	}
	if len(out.Properties) != 0 {
		t.Errorf("properties = %+v, want empty when absent", out.Properties)
	}
}

func TestGetRecipeScalesAmountsButNotNutrition(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":7,"name":"Soup","servings":2,
			"nutrition":{"calories":"250"},
			"steps":[{"instruction":"boil","ingredients":[{"amount":2,"food":{"name":"water"},"unit":{"name":"cup"},"no_amount":false}]}]}`)
	})
	four := 4
	res, _, err := h.getRecipe(context.Background(), nil, getRecipeInput{Recipe: "7", Servings: &four})
	if err != nil {
		t.Fatalf("getRecipe: %v", err)
	}
	var out getRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Servings != "4" {
		t.Errorf("servings = %q, want 4", out.Servings)
	}
	amt := out.Steps[0].Ingredients[0].Amount
	if amt == nil || *amt != 2 {
		t.Errorf("ingredient amount = %v, want stored amount 2 in structured steps", amt)
	}
	if !strings.Contains(out.Markdown, "4 cup water") {
		t.Errorf("markdown = %q, want scaled amount 4", out.Markdown)
	}
	if out.Nutrition == nil || out.Nutrition.Calories != "250" {
		t.Errorf("nutrition = %+v, want calories unchanged at 250 (not scaled)", out.Nutrition)
	}
}

// ---- check_shopping_items (bulk) ----

func TestCheckShoppingItemsBulk(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/shopping-list-entry/bulk/" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{}`)
	})
	res, _, err := h.checkShoppingItems(context.Background(), nil, checkShoppingInput{IDs: []int{1, 2, 3}})
	if err != nil {
		t.Fatalf("checkShoppingItems: %v", err)
	}
	ids, ok := body["ids"].([]any)
	if !ok || len(ids) != 3 || idx(t, at(t, body, "ids"), 0) != 1.0 {
		t.Errorf("ids = %v, want [1 2 3]", body["ids"])
	}
	if at(t, body, "checked") != true {
		t.Errorf("checked = %v, want true (default)", at(t, body, "checked"))
	}
	if !strings.Contains(resultText(t, res), `"count": 3`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestCheckShoppingItemsUncheck(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{}`)
	})
	no := false
	_, _, err := h.checkShoppingItems(context.Background(), nil, checkShoppingInput{IDs: []int{9}, Checked: &no})
	if err != nil {
		t.Fatalf("checkShoppingItems: %v", err)
	}
	if at(t, body, "checked") != false {
		t.Errorf("checked = %v, want false (uncheck)", at(t, body, "checked"))
	}
}

func TestCheckShoppingItemsRequiresIDs(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call backend with no ids")
	})
	if _, _, err := h.checkShoppingItems(context.Background(), nil, checkShoppingInput{}); err == nil {
		t.Error("expected error for empty ids")
	}
}
