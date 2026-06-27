package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- Fix #1: a natural-language line with no number must be no-amount, not "0 X" ---

func TestBuildIngredientParsedZeroIsNoAmount(t *testing.T) {
	// Real Tandoor returns a numeric 0 (not a Raw token) for "salt to taste".
	p := apiIngredient{Amount: flexNum{Set: true, Value: 0}, Food: &apiFood{Name: "salt"}, Note: "to taste"}
	m, err := buildIngredient(ingredientInput{Text: "salt to taste"}, &p)
	if err != nil {
		t.Fatal(err)
	}
	if m["no_amount"] != true {
		t.Errorf("no_amount = %v, want true (a parsed numeric 0 means no amount)", m["no_amount"])
	}
	if m["amount"] != 0 {
		t.Errorf("amount = %v, want 0", m["amount"])
	}
}

func TestCreateRecipeTextNoAmountStoresNoAmount(t *testing.T) {
	var recipeBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ingredient-parser/post/":
			// The shape the live parser actually returns for a no-number line.
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":0,"no_amount":false,"food":{"name":"salt"},"note":"to taste"}]}`)
		case "/api/recipe/":
			recipeBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":1,"name":"X","steps":[{}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "X", Instructions: "mix", Ingredients: []ingredientInput{{Text: "salt to taste"}},
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	ing := idx(t, at(t, idx(t, at(t, recipeBody, "steps"), 0), "ingredients"), 0)
	if at(t, ing, "no_amount") != true {
		t.Errorf("posted ingredient no_amount = %v, want true (else it renders \"0 salt\")", at(t, ing, "no_amount"))
	}
}

// --- Fix #2: a section header must never be parsed into a phantom food ---

func TestBuildIngredientHeaderFromText(t *testing.T) {
	// Header text supplied via `text` (not `note`) must still produce a header.
	m, err := buildIngredient(ingredientInput{IsHeader: true, Text: "For the sauce"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m["is_header"] != true || m["note"] != "For the sauce" {
		t.Errorf("got %v, want is_header:true note:'For the sauce'", m)
	}
	if m["food"] != nil {
		t.Errorf("food = %v, want nil for a header", m["food"])
	}
}

func TestCreateRecipeHeaderNotSentToParser(t *testing.T) {
	var parserLines []string
	var recipeBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ingredient-parser/post/":
			b := decodeBody(t, r)
			if arr, ok := b["ingredients"].([]any); ok {
				for _, v := range arr {
					parserLines = append(parserLines, v.(string))
				}
			}
			_, _ = io.WriteString(w, `{"ingredients":[{"amount":2,"unit":{"name":"cup"},"food":{"name":"flour"}}]}`)
		case "/api/recipe/":
			recipeBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":1,"name":"X","steps":[{}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "X",
		Steps: []stepInput{{Ingredients: []ingredientInput{
			{IsHeader: true, Text: "For the batter"},
			{Text: "2 cups flour"},
		}}},
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	for _, line := range parserLines {
		if strings.Contains(line, "For the batter") {
			t.Errorf("header text was sent to the ingredient parser (would create a phantom food): %q", line)
		}
	}
	hdr := idx(t, at(t, idx(t, at(t, recipeBody, "steps"), 0), "ingredients"), 0)
	if at(t, hdr, "is_header") != true {
		t.Errorf("first ingredient = %v, want a header", hdr)
	}
}

// --- Fix #3: a no-amount shopping entry must not render "0 X" ---

func TestGetShoppingListHonorsNoAmount(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"amount":0,"no_amount":true,"food":{"name":"salt"},"checked":false}]`)
	})
	res, _, err := h.getShoppingList(context.Background(), nil, getShoppingInput{})
	if err != nil {
		t.Fatalf("getShoppingList: %v", err)
	}
	txt := resultText(t, res)
	if strings.Contains(txt, "0 salt") {
		t.Errorf("no-amount entry rendered as \"0 salt\": %s", txt)
	}
	if !strings.Contains(txt, `"item": "salt"`) {
		t.Errorf("expected item line 'salt', got %s", txt)
	}
	if strings.Contains(txt, `"amount"`) {
		t.Errorf("no-amount entry should omit the structured amount field: %s", txt)
	}
}

func TestAddToShoppingListOmittedAmountIsNoAmount(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":1}`)
	})
	_, _, err := h.addToShoppingList(context.Background(), nil, addShoppingInput{Food: "olive oil"})
	if err != nil {
		t.Fatalf("addToShoppingList: %v", err)
	}
	if at(t, body, "no_amount") != true {
		t.Errorf("no_amount = %v, want true when amount omitted (not a silent default of 1)", at(t, body, "no_amount"))
	}
}

// --- Fix F-C: a single ingredient must not mix text and structured fields ---

func TestBuildIngredientRejectsTextAndStructured(t *testing.T) {
	amt := 2.0
	p := apiIngredient{Amount: flexNum{Set: true, Value: 2}, Food: &apiFood{Name: "flour"}}
	_, err := buildIngredient(ingredientInput{Text: "2 cups flour", Food: "flour", Amount: &amt, Unit: "cup"}, &p)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("err = %v, want a text-vs-structured rejection", err)
	}
}

// --- Fix F-A: providing both top-level ingredients and steps[] must error ---

func TestCreateRecipeRejectsBothIngredientsAndSteps(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not call the API when both ingredients and steps are given")
	})
	amt := 1.0
	_, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name:        "X",
		Ingredients: []ingredientInput{{Amount: &amt, Food: "flour"}},
		Steps:       []stepInput{{Instruction: "mix"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("err = %v, want a 'not both' rejection", err)
	}
}
