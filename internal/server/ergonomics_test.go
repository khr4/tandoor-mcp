package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- A: not-found errors carry a discovery hint ---

func TestResolveRecipeNotFoundHasHint(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	})
	_, err := h.resolveRecipe(context.Background(), "Nope")
	if err == nil || !strings.Contains(err.Error(), "find_recipes") {
		t.Errorf("err = %v, want a find_recipes discovery hint", err)
	}
}

func TestResolveTaxonomyNotFoundHasHint(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	})
	_, err := h.resolveTaxonomyID(context.Background(), "keyword", "Nope")
	if err == nil || !strings.Contains(err.Error(), "list_taxonomy") {
		t.Errorf("err = %v, want a list_taxonomy discovery hint", err)
	}
}

// --- D: create_recipe echoes the as-stored ingredient lines ---

func TestCreateRecipeEchoesStoredIngredients(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe/" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		// Tandoor returns the full created recipe, including a no-amount item.
		_, _ = io.WriteString(w, `{"id":1,"name":"X","steps":[{"ingredients":[
			{"amount":2,"unit":{"name":"cup"},"food":{"name":"flour"}},
			{"food":{"name":"salt"},"no_amount":true,"note":"to taste"}]}]}`)
	})
	amt := 2.0
	res, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "X", Instructions: "mix",
		Ingredients: []ingredientInput{{Amount: &amt, Unit: "cup", Food: "flour"}},
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	var out createRecipeOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out.Ingredients, " | ")
	if !strings.Contains(joined, "2 cup flour") || !strings.Contains(joined, "salt") {
		t.Errorf("ingredient echo = %v, want '2 cup flour' and 'salt'", out.Ingredients)
	}
	if strings.Contains(joined, "0 salt") {
		t.Errorf("echo renders a no-amount item with 0: %v", out.Ingredients)
	}
}

// --- C: set_food_on_hand processes a batch and deduplicates ---

func TestSetFoodsOnHandBatch(t *testing.T) {
	var patched []string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/food/":
			name, _ := at(t, decodeBody(t, r), "name").(string)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"id":%d,"name":%q}`, len(name), name))
		case r.Method == http.MethodPatch:
			patched = append(patched, r.URL.Path)
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.setFoodOnHand(context.Background(), nil, setOnhandInput{Foods: []string{"flour", "sugar", "butter"}})
	if err != nil {
		t.Fatalf("setFoodOnHand: %v", err)
	}
	if len(patched) != 3 {
		t.Errorf("patched %d foods, want 3: %v", len(patched), patched)
	}
	if !strings.Contains(resultText(t, res), `"status": "updated"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestSetFoodOnHandRejectsFoodAndFoodsTogether(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for ambiguous food selectors")
	})
	_, _, err := h.setFoodOnHand(context.Background(), nil, setOnhandInput{Food: "flour", Foods: []string{"Flour", "sugar"}})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want exact-one selector rejection", err)
	}
}
