package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

// newHandlersFunc wires handlers to a custom backend handler, letting each test
// route by method+path and assert on recorded request bodies.
func newHandlersFunc(t *testing.T, h http.HandlerFunc) *handlers {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := tandoor.New(tandoor.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &handlers{c: c}
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decoding body %s: %v", b, err)
	}
	return m
}

func at(t *testing.T, v any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("expected map at key %q, got %T", k, v)
		}
		v = m[k]
	}
	return v
}

func idx(t *testing.T, v any, i int) any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", v)
	}
	if i >= len(arr) {
		t.Fatalf("index %d out of range (len %d)", i, len(arr))
	}
	return arr[i]
}

// ---- pure helpers ----

func TestBuildIngredientStructuredKeepsQuantity(t *testing.T) {
	amt := 2.5
	m, err := buildIngredient(ingredientInput{Amount: &amt, Unit: "cup", Food: "flour", Note: "sifted"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m["amount"] != 2.5 {
		t.Errorf("amount = %v, want 2.5", m["amount"])
	}
	if at(t, m, "unit", "name") != "cup" {
		t.Errorf("unit = %v, want cup", m["unit"])
	}
	if at(t, m, "food", "name") != "flour" {
		t.Errorf("food = %v, want flour", m["food"])
	}
	if m["no_amount"] != false {
		t.Errorf("no_amount = %v, want false", m["no_amount"])
	}
}

func TestBuildIngredientNoAmount(t *testing.T) {
	m, err := buildIngredient(ingredientInput{Food: "salt", Note: "to taste"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m["no_amount"] != true {
		t.Errorf("no_amount = %v, want true", m["no_amount"])
	}
	if m["unit"] != nil {
		t.Errorf("unit = %v, want nil", m["unit"])
	}
}

func TestBuildIngredientRequiresFood(t *testing.T) {
	if _, err := buildIngredient(ingredientInput{Note: "x"}, nil); err == nil {
		t.Error("expected error when neither text nor food given")
	}
}

func TestBuildIngredientFromParsedSplitsQuantity(t *testing.T) {
	p := apiIngredient{
		Amount: flexNum{Set: true, Value: 2},
		Unit:   &apiUnit{Name: "cup"},
		Food:   &apiFood{Name: "flour"},
	}
	m, err := buildIngredient(ingredientInput{Text: "2 cups flour"}, &p)
	if err != nil {
		t.Fatal(err)
	}
	if m["amount"] != 2.0 {
		t.Errorf("amount = %v, want 2", m["amount"])
	}
	if at(t, m, "unit", "name") != "cup" || at(t, m, "food", "name") != "flour" {
		t.Errorf("unit/food = %v / %v", m["unit"], m["food"])
	}
	if m["original_text"] != "2 cups flour" {
		t.Errorf("original_text = %v", m["original_text"])
	}
}

func TestFlexNumParsesNumberStringNull(t *testing.T) {
	var s struct {
		A flexNum `json:"a"`
		B flexNum `json:"b"`
		C flexNum `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":2,"b":"1.5","c":null}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.A.String() != "2" || s.B.String() != "1.5" || s.C.String() != "" {
		t.Errorf("got %q %q %q", s.A, s.B, s.C)
	}
}

func TestFormatIngredientLine(t *testing.T) {
	line := formatIngredient(apiIngredient{
		Amount: flexNum{Set: true, Value: 2},
		Unit:   &apiUnit{Name: "cup"},
		Food:   &apiFood{Name: "flour"},
		Note:   "sifted",
	})
	if line != "2 cup flour (sifted)" {
		t.Errorf("line = %q, want \"2 cup flour (sifted)\"", line)
	}
}

// ---- create_recipe (quantity-critical path) ----

func TestCreateRecipeParsesLinesAndPostsExplicitQuantities(t *testing.T) {
	var recipeBody map[string]any
	var parserCalled bool
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/ingredient-parser/post/":
			parserCalled = true
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":2,"unit":{"name":"cup"},"food":{"name":"flour"},"no_amount":false,"note":""}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe/":
			recipeBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":1,"name":"Pancakes","steps":[{}],"keywords":[{"name":"Breakfast"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	egg := 3.0
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name:     "Pancakes",
		Keywords: []string{"Breakfast"},
		Steps: []stepInput{{
			Instruction: "mix",
			Ingredients: []ingredientInput{
				{Text: "2 cups flour"},
				{Amount: &egg, Food: "egg"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	if !parserCalled {
		t.Error("ingredient parser was not called for the natural-language line")
	}
	if at(t, recipeBody, "internal") != true {
		t.Errorf("internal = %v, want true", at(t, recipeBody, "internal"))
	}
	if kw := at(t, idx(t, at(t, recipeBody, "keywords"), 0), "name"); kw != "Breakfast" {
		t.Errorf("keyword = %v, want Breakfast", kw)
	}
	ings := at(t, idx(t, at(t, recipeBody, "steps"), 0), "ingredients")
	first := idx(t, ings, 0)
	if at(t, first, "amount") != 2.0 || at(t, first, "unit", "name") != "cup" || at(t, first, "food", "name") != "flour" {
		t.Errorf("parsed ingredient wrong: %v", first)
	}
	second := idx(t, ings, 1)
	if at(t, second, "amount") != 3.0 || at(t, second, "food", "name") != "egg" {
		t.Errorf("structured ingredient wrong: %v", second)
	}
	if at(t, second, "unit") != nil {
		t.Errorf("egg unit = %v, want nil", at(t, second, "unit"))
	}
}

// ---- find_recipes (name resolution + warnings) ----

func TestFindRecipesResolvesKeywords(t *testing.T) {
	var recipeQuery string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/keyword/":
			if strings.Contains(r.URL.RawQuery, "Vegan") {
				_, _ = io.WriteString(w, `{"results":[{"id":7,"name":"Vegan"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"results":[]}`)
			}
		case "/api/recipe/":
			recipeQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"count":1,"results":[{"id":5,"name":"Tofu Stir Fry","rating":"4.0","keywords":[{"name":"Vegan"}]}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	res, _, err := h.findRecipes(context.Background(), nil, findRecipesInput{
		Text:     "tofu",
		Keywords: []string{"Vegan"},
	})
	if err != nil {
		t.Fatalf("findRecipes: %v", err)
	}
	if !strings.Contains(recipeQuery, "keywords_and=7") || strings.Count(recipeQuery, "keywords_and=") != 1 {
		t.Errorf("recipe query = %q, want single keywords_and=7 (AND semantics)", recipeQuery)
	}
	if !strings.Contains(recipeQuery, "query=tofu") {
		t.Errorf("recipe query = %q, want query=tofu", recipeQuery)
	}
	var out findRecipesOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decoding output: %v", err)
	}
	if len(out.Recipes) != 1 || out.Recipes[0].Name != "Tofu Stir Fry" {
		t.Errorf("recipes = %+v", out.Recipes)
	}
}

func TestFindRecipesFailsOnUnresolvedFilter(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/recipe/" {
			t.Fatal("recipe search must not run after an unresolved required filter")
		}
		_, _ = io.WriteString(w, `{"results":[]}`)
	})
	if _, _, err := h.findRecipes(context.Background(), nil, findRecipesInput{Keywords: []string{"Nonexistent"}}); err == nil {
		t.Fatal("expected unresolved keyword to fail closed")
	}
}

// ---- import_recipe_from_url ----

func TestImportSavesScrapedRecipe(t *testing.T) {
	var recipeBody map[string]any
	scrape := `{"recipe":{"name":"Soup","servings":2,"steps":[{"instruction":"boil","ingredients":[{"amount":1,"food":{"name":"water"},"unit":{"name":"l"},"original_text":"1 l water"}]}],"keywords":[{"name":"easy"}]},"images":[],"duplicates":[]}`
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/recipe-from-source/":
			_, _ = io.WriteString(w, scrape)
		case "/api/recipe/":
			recipeBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":9,"name":"Soup"}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if at(t, recipeBody, "name") != "Soup" || at(t, recipeBody, "source_url") != "https://recipes.example.com/soup" {
		t.Errorf("recipe body name/source wrong: %v", recipeBody)
	}
	ing := idx(t, at(t, idx(t, at(t, recipeBody, "steps"), 0), "ingredients"), 0)
	if at(t, ing, "amount") != 1.0 || at(t, ing, "food", "name") != "water" || at(t, ing, "unit", "name") != "l" {
		t.Errorf("imported ingredient wrong: %v", ing)
	}
	if !strings.Contains(resultText(t, res), `"status": "imported"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestImportPreviewDoesNotSave(t *testing.T) {
	posted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/recipe/" {
			posted = true
		}
		_, _ = io.WriteString(w, `{"recipe":{"name":"Soup","steps":[]},"images":[],"duplicates":[]}`)
	})
	save := false
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/source", Save: &save})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if posted {
		t.Error("preview must not POST to /api/recipe/")
	}
	if !strings.Contains(resultText(t, res), `"status": "preview"`) {
		t.Errorf("expected preview, got %s", resultText(t, res))
	}
}

// ---- plan_meal ----

func TestPlanMealSendsDatetimeBareIntAndDefaultServings(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":3,"from_date":"2026-07-01T00:00:00","meal_type":{"name":"Dinner"},"recipe":{"id":5,"name":"Tofu"}}`)
	})
	// recipe "5" is a numeric reference, so no lookup HTTP is needed.
	_, _, err := h.planMeal(context.Background(), nil, planMealInput{Date: "2026-07-01", MealType: "Dinner", Recipe: "5"})
	if err != nil {
		t.Fatalf("planMeal: %v", err)
	}
	if at(t, body, "from_date") != "2026-07-01T00:00:00" {
		t.Errorf("from_date = %v, want datetime (DateTimeField rejects date-only)", at(t, body, "from_date"))
	}
	if at(t, body, "meal_type", "name") != "Dinner" {
		t.Errorf("meal_type = %v", at(t, body, "meal_type"))
	}
	if at(t, body, "recipe") != 5.0 {
		t.Errorf("recipe = %v, want bare int 5", at(t, body, "recipe"))
	}
	if at(t, body, "servings") != 1.0 {
		t.Errorf("servings = %v, want default 1", at(t, body, "servings"))
	}
}

// ---- shopping ----

func TestAddToShoppingListBody(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":1}`)
	})
	amt := 2.0
	_, _, err := h.addToShoppingList(context.Background(), nil, addShoppingInput{Food: "milk", Amount: &amt, Unit: "l"})
	if err != nil {
		t.Fatalf("addToShoppingList: %v", err)
	}
	if at(t, body, "food", "name") != "milk" || at(t, body, "amount") != 2.0 || at(t, body, "unit", "name") != "l" {
		t.Errorf("body wrong: %v", body)
	}
	if at(t, body, "checked") != false {
		t.Errorf("checked = %v, want false", at(t, body, "checked"))
	}
}

func TestGetShoppingListFiltersChecked(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"amount":"2","unit":{"name":"l"},"food":{"name":"milk"},"checked":false},{"id":2,"amount":"1","food":{"name":"bread"},"checked":true}]`)
	})
	res, _, err := h.getShoppingList(context.Background(), nil, getShoppingInput{})
	if err != nil {
		t.Fatalf("getShoppingList: %v", err)
	}
	var out shoppingListOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Item != "2 l milk" || out.Items[0].Food != "milk" || out.Items[0].Unit != "l" {
		t.Errorf("items = %+v, want one structured '2 l milk'", out.Items)
	}
}

// ---- pantry ----

func TestSetFoodOnHandGetOrCreatesAndPatches(t *testing.T) {
	var patchPath string
	var postBody, patchBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/food/":
			postBody = decodeBody(t, r) // server-side get-or-create by name
			_, _ = io.WriteString(w, `{"id":4,"name":"Milk"}`)
		case r.Method == http.MethodPatch:
			patchPath = r.URL.Path
			patchBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":4}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	_, _, err := h.setFoodOnHand(context.Background(), nil, setOnhandInput{Food: "Milk"})
	if err != nil {
		t.Fatalf("setFoodOnHand: %v", err)
	}
	if at(t, postBody, "name") != "Milk" {
		t.Errorf("create body = %v, want name=Milk", postBody)
	}
	if patchPath != "/api/food/4/" {
		t.Errorf("patch path = %q, want /api/food/4/", patchPath)
	}
	if at(t, patchBody, "food_onhand") != true {
		t.Errorf("food_onhand = %v, want true", at(t, patchBody, "food_onhand"))
	}
}

func TestSetFoodOnHandClearDoesNotCreate(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Fatal("clearing on-hand state must not create a food")
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
	})
	onHand := false
	if _, _, err := h.setFoodOnHand(context.Background(), nil, setOnhandInput{Food: "Typo", OnHand: &onHand}); err == nil {
		t.Fatal("expected clearing a missing food to fail")
	}
}
