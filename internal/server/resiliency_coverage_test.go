package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

func TestUpdateMealPlanEntryBuildsPatchAndClearsFields(t *testing.T) {
	var patchBodies []map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe/":
			if !strings.Contains(r.URL.RawQuery, "Stew") {
				t.Fatalf("recipe resolve query = %q, want Stew", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":7,"name":"Stew"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/meal-plan/9/":
			patchBodies = append(patchBodies, decodeBody(t, r))
			_, _ = io.WriteString(w, `{"id":9,"from_date":"2026-07-01T00:00:00","to_date":"2026-07-03T00:00:00","meal_type":{"id":2,"name":"Dinner"},"recipe":{"id":7,"name":"Stew"},"servings":4,"title":"Holiday","note":"prep"}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	date, endDate, mealType, recipe := "2026-07-01", "2026-07-03", "Dinner", "Stew"
	servings := 4
	title, note := "Holiday", "prep"
	res, _, err := h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{
		ID: 9, Date: &date, EndDate: &endDate, MealType: &mealType, Recipe: &recipe,
		Servings: &servings, Title: &title, Note: &note,
	})
	if err != nil {
		t.Fatalf("updateMealPlanEntry: %v", err)
	}
	if len(patchBodies) != 1 {
		t.Fatalf("patch bodies = %d, want 1", len(patchBodies))
	}
	body := patchBodies[0]
	if at(t, body, "from_date") != "2026-07-01T00:00:00" || at(t, body, "to_date") != "2026-07-03T00:00:00" {
		t.Fatalf("date body = %v", body)
	}
	if at(t, body, "meal_type", "name") != "Dinner" || at(t, body, "recipe") != 7.0 || at(t, body, "servings") != 4.0 {
		t.Fatalf("linked fields body = %v", body)
	}
	if at(t, body, "title") != "Holiday" || at(t, body, "note") != "prep" {
		t.Fatalf("text fields body = %v", body)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "updated" || at(t, out, "entry", "recipe") != "Stew" {
		t.Fatalf("structuredContent = %v", out)
	}

	empty := ""
	res, _, err = h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{
		ID: 9, Recipe: &empty, Title: &empty, Note: &empty,
	})
	if err != nil {
		t.Fatalf("updateMealPlanEntry clear fields: %v", err)
	}
	body = patchBodies[1]
	if body["recipe"] != nil || at(t, body, "title") != "" || at(t, body, "note") != "" {
		t.Fatalf("clear body = %v, want recipe nil and empty title/note", body)
	}
	if structuredContentMap(t, res)["status"] != "updated" {
		t.Fatalf("clear result = %s", resultText(t, res))
	}
}

func TestUpdateMealPlanEntryRejectsNoopAndInvalidInputs(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid meal-plan updates")
	})
	if _, _, err := h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{ID: 0}); err == nil {
		t.Fatal("expected id rejection")
	}
	if _, _, err := h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{ID: 9}); err == nil {
		t.Fatal("expected no-op rejection")
	}
	badDate := ""
	if _, _, err := h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{ID: 9, Date: &badDate}); err == nil {
		t.Fatal("expected blank date rejection")
	}
	zero := 0
	if _, _, err := h.updateMealPlanEntry(context.Background(), nil, updateMealPlanInput{ID: 9, Servings: &zero}); err == nil {
		t.Fatal("expected non-positive servings rejection")
	}
}

func TestPlanMealNamedRecipeEndDateTitleNote(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":5,"name":"Soup"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/meal-plan/":
			body = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":3,"from_date":"2026-08-01T00:00:00","to_date":"2026-08-02T00:00:00","meal_type":{"name":"Lunch"},"recipe":{"id":5,"name":"Soup"},"servings":2,"title":"Trip","note":"thermos"}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	servings := 2
	res, _, err := h.planMeal(context.Background(), nil, planMealInput{
		Date: "2026-08-01", EndDate: "2026-08-02", MealType: "Lunch",
		Recipe: "Soup", Servings: &servings, Title: "Trip", Note: "thermos",
	})
	if err != nil {
		t.Fatalf("planMeal: %v", err)
	}
	if at(t, body, "from_date") != "2026-08-01T00:00:00" || at(t, body, "to_date") != "2026-08-02T00:00:00" {
		t.Fatalf("date body = %v", body)
	}
	if at(t, body, "meal_type", "name") != "Lunch" || at(t, body, "recipe") != 5.0 || at(t, body, "servings") != 2.0 {
		t.Fatalf("body = %v", body)
	}
	if at(t, body, "title") != "Trip" || at(t, body, "note") != "thermos" {
		t.Fatalf("body = %v", body)
	}
	if at(t, structuredContentMap(t, res), "entry", "recipe") != "Soup" {
		t.Fatalf("result = %s", resultText(t, res))
	}
}

func TestGetInventoryResolvesLocationFilterByName(t *testing.T) {
	var inventoryQueried bool
	var query string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/inventory-location/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":12,"name":"Cellar"}]}`)
		case "/api/inventory-entry/":
			inventoryQueried = true
			query = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"count":0,"next":null,"results":[]}`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	})
	res, _, err := h.getInventory(context.Background(), nil, getInventoryInput{Location: "Cellar"})
	if err != nil {
		t.Fatalf("getInventory: %v", err)
	}
	if !inventoryQueried || !strings.Contains(query, "inventory_location_id=12") {
		t.Fatalf("inventory query = %q, queried=%v", query, inventoryQueried)
	}
	if structuredContentMap(t, res)["location_id"] != 12.0 {
		t.Fatalf("result = %s", resultText(t, res))
	}

	h = newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/inventory-entry/" {
			t.Fatal("inventory entries must not be queried after unresolved location")
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
	})
	if _, _, err := h.getInventory(context.Background(), nil, getInventoryInput{Location: "Missing"}); err == nil {
		t.Fatal("expected missing location rejection")
	}
}

func TestCreateTaxonomyParentMoveFailureReturnsPartial(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/food/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":7,"name":"Vegetables"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/food/":
			_, _ = io.WriteString(w, `{"id":9,"name":"Carrot"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/food/9/move/7/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarting`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.createTaxonomy(context.Background(), nil, createTaxonomyInput{Kind: "food", Name: "Carrot", Parent: "Vegetables"})
	if err != nil {
		t.Fatalf("createTaxonomy: %v", err)
	}
	if !res.IsError {
		t.Fatal("parent move failure after create must be an MCP error result")
	}
	out := structuredContentMap(t, res)
	if out["status"] != "partial" || out["phase"] != "move_parent" || out["parent_id"] != 7.0 {
		t.Fatalf("structuredContent = %v", out)
	}
	if at(t, out, "failure", "status") != "outcome_unknown" {
		t.Fatalf("failure = %v, want outcome_unknown", out["failure"])
	}
}

func TestRenameTaxonomyNameResolvedPluralDescriptionAndValidation(t *testing.T) {
	var patchBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/food/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":5,"name":"Tomato"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/food/5/":
			patchBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":5,"name":"Tomato","plural_name":"Tomatoes","description":"red"}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	name, plural, desc := "Tomato", "Tomatoes", "red"
	res, _, err := h.renameTaxonomy(context.Background(), nil, renameTaxonomyInput{
		Kind: "food", Item: "Tomato", Name: &name, PluralName: &plural, Description: &desc,
	})
	if err != nil {
		t.Fatalf("renameTaxonomy: %v", err)
	}
	if at(t, patchBody, "name") != "Tomato" || at(t, patchBody, "plural_name") != "Tomatoes" || at(t, patchBody, "description") != "red" {
		t.Fatalf("patchBody = %v", patchBody)
	}
	if at(t, structuredContentMap(t, res), "item", "plural_name") != "Tomatoes" {
		t.Fatalf("result = %s", resultText(t, res))
	}

	h = newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid taxonomy rename")
	})
	if _, _, err := h.renameTaxonomy(context.Background(), nil, renameTaxonomyInput{Kind: "food", Item: "5"}); err == nil {
		t.Fatal("expected no-field rename rejection")
	}
	if _, _, err := h.renameTaxonomy(context.Background(), nil, renameTaxonomyInput{Kind: "keyword", Item: "5", PluralName: &plural}); err == nil {
		t.Fatal("expected keyword plural_name rejection")
	}
}

func TestMergeTaxonomyOutcomeUnknownVerifiesPostcondition(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/keyword/3/merge/4/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarting`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/keyword/3/":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `missing`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.mergeTaxonomy(context.Background(), nil, mergeTaxonomyInput{Kind: "keyword", Source: "3", Target: "4"})
	if err != nil {
		t.Fatalf("mergeTaxonomy: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "merged" || out["verified_after_unknown"] != true {
		t.Fatalf("structuredContent = %v", out)
	}
}

func TestMergeTaxonomyOutcomeUnknownReportsPostconditionError(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/keyword/3/merge/4/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarting`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/keyword/3/":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `gateway down`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.mergeTaxonomy(context.Background(), nil, mergeTaxonomyInput{Kind: "keyword", Source: "3", Target: "4"})
	if err != nil {
		t.Fatalf("mergeTaxonomy: %v", err)
	}
	if !res.IsError {
		t.Fatal("postcondition lookup failure must keep the merge ambiguous")
	}
	out := structuredContentMap(t, res)
	if out["status"] != "outcome_unknown" || out["postcondition_error"] == "" {
		t.Fatalf("structuredContent = %v", out)
	}
}

func TestUpdateRecipeScalarPatchWithRevisionAndURLValidation(t *testing.T) {
	recipeRaw := `{"id":5,"name":"Old","description":"before","keywords":[]}`
	var patchBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe/5/":
			_, _ = io.WriteString(w, recipeRaw)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/recipe/5/":
			patchBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":5,"name":"New","description":"","source_url":"","servings":3,"working_time":10,"waiting_time":20,"keywords":[]}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	name, description, sourceURL := "New", "", ""
	servings, working, waiting := 3, 10, 20
	res, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{
		Recipe: "5", ExpectedRevision: recipeRevision([]byte(recipeRaw)),
		Name: &name, Description: &description, SourceURL: &sourceURL,
		Servings: &servings, WorkingTime: &working, WaitingTime: &waiting,
	})
	if err != nil {
		t.Fatalf("updateRecipe: %v", err)
	}
	for _, field := range []string{"name", "description", "source_url", "servings", "working_time", "waiting_time"} {
		if _, ok := patchBody[field]; !ok {
			t.Fatalf("patch body missing %s: %v", field, patchBody)
		}
	}
	out := structuredContentMap(t, res)
	if out["edit_revision"] == "" || out["recipe_id"] != 5.0 {
		t.Fatalf("structuredContent = %v", out)
	}

	h = newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for unsafe source_url")
	})
	privateURL := "http://127.0.0.1/recipe"
	if _, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{Recipe: "5", SourceURL: &privateURL}); err == nil {
		t.Fatal("expected private source_url rejection")
	}
}

func TestUpdateRecipeKeywordEditFailsClosedOnMissingOrStaleRevision(t *testing.T) {
	recipeRaw := `{"id":5,"name":"Soup","keywords":[]}`
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			t.Fatal("stale keyword update must not patch")
		}
		_, _ = io.WriteString(w, recipeRaw)
	})
	if _, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{Recipe: "5", AddKeywords: []string{"vegan"}}); err == nil {
		t.Fatal("expected missing revision rejection")
	}
	staleRevision := recipeRevision([]byte(`{"id":5,"name":"Old","keywords":[]}`))
	if _, _, err := h.updateRecipe(context.Background(), nil, updateRecipeInput{
		Recipe: "5", ExpectedRevision: staleRevision, AddKeywords: []string{"vegan"},
	}); err == nil || !strings.Contains(err.Error(), "changed since get_recipe") {
		t.Fatalf("err = %v, want stale revision rejection", err)
	}
}

func TestCreateRecipeRejectsAmbiguousInputsAndUnsafeSource(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid recipe create input")
	})
	if _, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "Soup", Ingredients: []ingredientInput{{Food: "salt"}}, Steps: []stepInput{{Instruction: "mix"}},
	}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v, want ambiguous ingredients/steps rejection", err)
	}
	if _, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "Soup", SourceURL: "http://127.0.0.1/recipe",
	}); err == nil {
		t.Fatal("expected unsafe source URL rejection")
	}
	longDescription := strings.Repeat("x", maxFreeTextRunes+1)
	if _, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "Soup", Description: longDescription,
	}); err == nil {
		t.Fatal("expected overlong description rejection")
	}
}

func TestCreateRecipePostsMetadataFields(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/recipe/" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":9,"name":"Soup","description":"warm","source_url":"https://recipes.example.com/soup","servings":4,"working_time":10,"waiting_time":30,"steps":[]}`)
	})
	servings, working, waiting := 4, 10, 30
	res, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{
		Name: "Soup", Description: "warm", SourceURL: "https://recipes.example.com/soup",
		Servings: &servings, WorkingTime: &working, WaitingTime: &waiting,
	})
	if err != nil {
		t.Fatalf("createRecipe: %v", err)
	}
	for _, field := range []string{"description", "source_url", "servings", "working_time", "waiting_time"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("create body missing %s: %v", field, body)
		}
	}
	out := structuredContentMap(t, res)
	if out["status"] != "created" || out["recipe_id"] != 9.0 || out["source_url"] != "https://recipes.example.com/soup" {
		t.Fatalf("structuredContent = %v", out)
	}
}

func TestSetRecipeStepsRequiresRevisionBeforeParsing(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called without expected_revision")
	})
	if _, _, err := h.setRecipeSteps(context.Background(), nil, setRecipeStepsInput{
		Recipe: "5",
		Steps:  []stepInput{{Instruction: "mix", Ingredients: []ingredientInput{{Text: "1 tsp salt"}}}},
	}); err == nil || !strings.Contains(err.Error(), "expected_revision") {
		t.Fatalf("err = %v, want expected_revision rejection", err)
	}
}

func TestRemoveMealPlanEntryRejectsInvalidID(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid meal-plan id")
	})
	if _, _, err := h.removeMealPlanEntry(context.Background(), nil, removeMealPlanInput{ID: -1}); err == nil {
		t.Fatal("expected invalid id rejection")
	}
}

func TestGenericBodyValidationRejectsBreadthAndKeyBypass(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid generic body")
	})
	tooMany := make([]any, maxGenericArrayItems+1)
	if _, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword", Data: map[string]any{"items": tooMany}}); err == nil {
		t.Fatal("expected oversized array rejection")
	}
	tooWide := map[string]any{}
	for i := 0; i <= maxGenericObjectKeys; i++ {
		tooWide[fmt.Sprintf("k%d", i)] = i
	}
	if _, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword", Data: tooWide}); err == nil {
		t.Fatal("expected too-many-keys rejection")
	}
	for _, key := range []string{"bad/key", "bad.key"} {
		if _, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword", Data: map[string]any{key: "x"}}); err == nil {
			t.Fatalf("expected invalid key rejection for %q", key)
		}
	}
	largeBody := map[string]any{"blob": strings.Repeat("x", maxGenericBodyBytes)}
	if _, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword", Data: largeBody}); err == nil {
		t.Fatal("expected marshaled body size rejection")
	}
}

func TestToolErrorCauseFieldsForNotAttemptedBreakerAndOutcomeUnknownAPI(t *testing.T) {
	res, _, err := toolErrorResult(&tandoor.NotAttemptedError{
		Method: http.MethodPost,
		Path:   "recipe/",
		Cause:  &tandoor.BreakerOpenError{RetryAfter: 1500 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("toolErrorResult: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "not_attempted" || out["cause_status"] != "breaker_open" || out["retry_after_ms"] != 1500.0 {
		t.Fatalf("not_attempted structuredContent = %v", out)
	}

	res, _, err = toolErrorResult(&tandoor.OutcomeUnknownError{
		Method: http.MethodPost,
		Path:   "recipe/",
		Cause: &tandoor.APIError{
			StatusCode: http.StatusServiceUnavailable,
			Method:     http.MethodPost,
			Path:       "recipe/",
			Body:       strings.Repeat("x", toolErrorExcerptRunes+10),
			RetryAfter: 2 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("toolErrorResult outcome unknown: %v", err)
	}
	out = structuredContentMap(t, res)
	if out["status"] != "outcome_unknown" || out["cause_status"] != "upstream_error" || out["status_code"] != 503.0 || out["retry_after_ms"] != 2000.0 || out["body_truncated"] != true {
		t.Fatalf("outcome_unknown structuredContent = %v", out)
	}
}

func TestReadyzJSONContentTypeAndSanitizedFailures(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), "secret", nil))
	t.Cleanup(front.Close)
	resp, err := http.Get(front.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || strings.TrimSpace(string(body)) != `{"status":"not_configured"}` {
		t.Fatalf("not configured status=%d body=%q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want JSON", ct)
	}

	front.Config.Handler = newHTTPHandler(mcpServer(t, nil), "secret", func(context.Context) error {
		return errors.New("upstream stack trace with confusing payload")
	})
	resp, err = http.Get(front.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	closeBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || strings.TrimSpace(string(body)) != `{"status":"unready"}` {
		t.Fatalf("unready status=%d body=%q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "stack trace") {
		t.Fatalf("readyz leaked upstream error: %s", body)
	}
}

func TestReadyProbeConcurrentRequestsSingleflightAndCachesSuccess(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	probe := newReadyProbe(func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return nil
	})

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- probe.checkReady(context.Background())
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	for got := range results {
		if !got {
			t.Fatal("ready check returned false, want true")
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want concurrent checks to share one probe", calls)
	}
	if !probe.checkReady(context.Background()) || calls != 1 {
		t.Fatalf("success cache missed: calls=%d", calls)
	}
}

func TestHTTPAuthGateProtectsSSE(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), "secret", nil))
	t.Cleanup(front.Close)
	for _, auth := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/sse", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		closeBody(t, resp)
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatalf("auth %q status=%d challenge=%q, want 401 with challenge", auth, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
}

func TestMCPGenericMutationOutcomeUnknownOverTransport(t *testing.T) {
	var attempts int32
	client := connect(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/keyword/" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `db restarting`)
	})
	ctx := context.Background()
	res, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "tandoor_create",
		Arguments: map[string]any{
			"resource": "keyword",
			"data":     map[string]any{"name": "Vegan"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("result IsError=false: %s", toolText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(toolText(res)), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "outcome_unknown" || out["retry_after_ms"] != 2000.0 || attempts != 1 {
		t.Fatalf("out = %v attempts=%d", out, attempts)
	}
}

func TestAddRecipeToBookDuplicatePostRechecksMembership(t *testing.T) {
	entryLookups := 0
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Favorites"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			entryLookups++
			if entryLookups == 1 {
				_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":11,"book":3,"recipe":5}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"non_field_errors":["recipe book entry already exists"]}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Favorites"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "already_in_book" || out["membership_id"] != 11.0 || entryLookups < 2 {
		t.Fatalf("structuredContent = %v entryLookups=%d", out, entryLookups)
	}
}

func TestAddRecipeToBookCreateOutcomeUnknownResolvesCreatedBook(t *testing.T) {
	bookLookups := 0
	entryLookups := 0
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			bookLookups++
			if bookLookups == 1 {
				_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":7,"name":"Weeknight"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarted after create`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			entryLookups++
			if entryLookups == 1 {
				_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":12,"book":7,"recipe":5}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			_, _ = io.WriteString(w, `{"id":12,"book":7,"recipe":5}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Weeknight"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "added" || out["book_id"] != 7.0 || out["membership_id"] != 12.0 || bookLookups < 2 {
		t.Fatalf("structuredContent = %v bookLookups=%d", out, bookLookups)
	}
}

func TestAddRecipeToBookMembershipOutcomeUnknownVerified(t *testing.T) {
	entryLookups := 0
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Favorites"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			entryLookups++
			if entryLookups == 1 {
				_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":11,"book":3,"recipe":5}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarted after membership create`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Favorites"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "already_in_book" || out["membership_id"] != 11.0 || entryLookups < 2 {
		t.Fatalf("structuredContent = %v entryLookups=%d", out, entryLookups)
	}
}

func TestListRecipeBooksMarksTruncatedFirstPage(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe-book/" || !strings.Contains(r.URL.RawQuery, "page_size=200") {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"count":201,"next":"next-page","results":[{"id":3,"name":"Favorites"}]}`)
	})
	res, _, err := h.listRecipeBooks(context.Background(), nil, listRecipeBooksInput{})
	if err != nil {
		t.Fatalf("listRecipeBooks: %v", err)
	}
	var out listRecipeBooksOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || !strings.Contains(out.Note, "first page") || len(out.Books) != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetCookLogPaginatesObjectRecipeAndTruncatesAtLimit(t *testing.T) {
	var queries []string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cook-log/" {
			t.Fatalf("unexpected %s", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"next":"page-2","results":[{"id":1,"recipe":{"id":5,"name":"Soup"},"rating":5,"servings":2,"created_at":"2026-06-01T12:00:00Z"},{"id":2,"recipe":6,"rating":2,"created_at":"2026-06-02T12:00:00Z"}]}`)
	})
	limit := 1
	res, _, err := h.getCookLog(context.Background(), nil, getCookLogInput{Limit: &limit})
	if err != nil {
		t.Fatalf("getCookLog: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["truncated"] != true || at(t, out, "summary", "count") != 1.0 || at(t, idx(t, out["logs"], 0), "recipe") != "Soup" {
		t.Fatalf("structuredContent = %v", out)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], "page=1") || !strings.Contains(queries[0], "page_size=200") {
		t.Fatalf("queries = %v", queries)
	}
}

func TestGetCookLogRejectsInvalidFiltersBeforeBackend(t *testing.T) {
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid cook-log filters")
	})
	badLimit := maxCookLogLimit + 1
	if _, _, err := h.getCookLog(context.Background(), nil, getCookLogInput{Limit: &badLimit}); err == nil {
		t.Fatal("expected limit rejection")
	}
	badRating := 6
	if _, _, err := h.getCookLog(context.Background(), nil, getCookLogInput{MinRating: &badRating}); err == nil {
		t.Fatal("expected min_rating rejection")
	}
}

func TestImportRecipeOutcomeUnknownOnScrapeAndCreate(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe-from-source/" {
			t.Fatalf("unexpected %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `scraper restarting`)
	})
	res, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup"})
	if err != nil {
		t.Fatalf("importRecipeFromURL scrape unknown: %v", err)
	}
	if out := structuredContentMap(t, res); out["status"] != "outcome_unknown" || out["operation"] != "import_recipe_from_url" {
		t.Fatalf("scrape structuredContent = %v", out)
	}

	h = newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-from-source/":
			_, _ = io.WriteString(w, `{"recipe":{"name":"Soup","steps":[]},"duplicates":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe/":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `db restarting`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":42,"name":"Soup"}]}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err = h.importRecipeFromURL(context.Background(), nil, importRecipeInput{URL: "https://recipes.example.com/soup"})
	if err != nil {
		t.Fatalf("importRecipeFromURL create unknown: %v", err)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "outcome_unknown" || out["operation"] != "import_recipe_from_url_create" {
		t.Fatalf("create structuredContent = %v", out)
	}
	candidates, ok := out["candidate_recipe_ids"].([]any)
	if !ok || len(candidates) != 1 || candidates[0] != 42.0 {
		t.Fatalf("candidate_recipe_ids = %v", out["candidate_recipe_ids"])
	}
}

func TestLogCookedPostsCommentServingsAndRejectsInvalids(t *testing.T) {
	var body map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cook-log/" {
			t.Fatalf("unexpected %s", r.URL.Path)
		}
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"id":1}`)
	})
	rating, servings := 4, 3
	res, _, err := h.logCooked(context.Background(), nil, logCookedInput{
		Recipe: "5", Rating: &rating, Servings: &servings, Comment: "served with salad",
	})
	if err != nil {
		t.Fatalf("logCooked: %v", err)
	}
	if at(t, body, "recipe") != 5.0 || at(t, body, "rating") != 4.0 || at(t, body, "servings") != 3.0 || at(t, body, "comment") != "served with salad" {
		t.Fatalf("body = %v", body)
	}
	if structuredContentMap(t, res)["rating"] != 4.0 {
		t.Fatalf("result = %s", resultText(t, res))
	}

	h = newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for invalid cook-log input")
	})
	badRating, zeroServings := 6, 0
	if _, _, err := h.logCooked(context.Background(), nil, logCookedInput{Recipe: "5", Rating: &badRating}); err == nil {
		t.Fatal("expected rating rejection")
	}
	if _, _, err := h.logCooked(context.Background(), nil, logCookedInput{Recipe: "5", Servings: &zeroServings}); err == nil {
		t.Fatal("expected servings rejection")
	}
}

func TestSetRecipeImageURLUpstreamErrorIncludesHint(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/recipe/5/image/" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `remote image fetch failed`)
	})
	res, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{
		Recipe:   "5",
		ImageURL: "https://images.example.com/soup.png",
	})
	if err != nil {
		t.Fatalf("setRecipeImage: %v", err)
	}
	if !res.IsError {
		t.Fatal("upstream image failure should be an MCP error result")
	}
	out := structuredContentMap(t, res)
	if out["status"] != "outcome_unknown" || out["operation"] != "set_recipe_image_url" || out["image_url_host"] != "images.example.com" {
		t.Fatalf("structuredContent = %v", out)
	}
	hint, _ := out["hint"].(string)
	if !strings.Contains(hint, "Tandoor fetches") || !strings.Contains(hint, "image_base64") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestSetRecipeImageURLValidationErrorIncludesHint(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/recipe/5/image/" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"image_url":["unsupported image"]}`)
	})
	res, _, err := h.setRecipeImage(context.Background(), nil, recipeImageInput{
		Recipe:   "5",
		ImageURL: "https://images.example.com/not-image",
	})
	if err != nil {
		t.Fatalf("setRecipeImage: %v", err)
	}
	if !res.IsError {
		t.Fatal("Tandoor image_url validation failure should be an MCP error result")
	}
	out := structuredContentMap(t, res)
	if out["status"] != "upstream_error" || out["status_code"] != 400.0 || out["operation"] != "set_recipe_image_url" {
		t.Fatalf("structuredContent = %v", out)
	}
	if !strings.Contains(out["body_excerpt"].(string), "unsupported image") {
		t.Fatalf("body excerpt = %v", out["body_excerpt"])
	}
	hint, _ := out["hint"].(string)
	if !strings.Contains(hint, "Tandoor fetches") || !strings.Contains(hint, "image_base64") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestOpenSafeImageRejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "not-an-image")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for a non-regular local image path")
	})
	h.imageDir = dir
	if _, _, err := h.openSafeImage(subdir); err == nil || !errors.Is(err, errImagePathDenied) {
		t.Fatalf("err = %v, want errImagePathDenied", err)
	}
}

func TestGetPantryStopsAtPageCapWithTruncation(t *testing.T) {
	var reqs int
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/food/" || !strings.Contains(r.URL.RawQuery, "page_size=200") {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		reqs++
		_, _ = io.WriteString(w, `{"next":"again","results":[{"id":1,"name":"Milk","food_onhand":true},{"id":2,"name":"Flour","food_onhand":false}]}`)
	})
	res, _, err := h.getPantry(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("getPantry: %v", err)
	}
	var out pantryOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if reqs != pantryScanPages || !out.Truncated || len(out.OnHand) != pantryScanPages || out.OnHand[0].Name != "Milk" {
		t.Fatalf("reqs=%d out=%+v", reqs, out)
	}
}

func TestSetFoodOnHandClearsExistingWithoutCreating(t *testing.T) {
	var patched []map[string]any
	posted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/food/":
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":8,"name":"Milk"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/food/8/":
			patched = append(patched, decodeBody(t, r))
			_, _ = io.WriteString(w, `{"id":8,"name":"Milk","food_onhand":false}`)
		case r.Method == http.MethodPost:
			posted = true
			t.Fatal("clearing on-hand state must not create missing foods")
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	no := false
	res, _, err := h.setFoodOnHand(context.Background(), nil, setOnhandInput{Foods: []string{"Milk", "milk", "  "}, OnHand: &no})
	if err != nil {
		t.Fatalf("setFoodOnHand: %v", err)
	}
	if posted || len(patched) != 1 || at(t, patched[0], "food_onhand") != false {
		t.Fatalf("posted=%v patched=%v", posted, patched)
	}
	out := structuredContentMap(t, res)
	if out["status"] != "updated" || out["on_hand"] != false || len(out["foods"].([]any)) != 1 {
		t.Fatalf("structuredContent = %v", out)
	}

	h = newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch || r.Method == http.MethodPost {
			t.Fatal("missing food must not be created or patched while clearing")
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[]}`)
	})
	res, _, err = h.setFoodOnHand(context.Background(), nil, setOnhandInput{Food: "Ghost", OnHand: &no})
	if err != nil {
		t.Fatalf("setFoodOnHand missing clear: %v", err)
	}
	if !res.IsError || structuredContentMap(t, res)["status"] != "failed" {
		t.Fatalf("result = %s", resultText(t, res))
	}
}

func TestMoveTaxonomyResolvesNamedParentAndRejectsSameID(t *testing.T) {
	var movePath string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/keyword/":
			if strings.Contains(r.URL.RawQuery, "Child") {
				_, _ = io.WriteString(w, `{"next":null,"results":[{"id":3,"name":"Child"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"next":null,"results":[{"id":4,"name":"Parent"}]}`)
		case r.Method == http.MethodPut:
			movePath = r.URL.Path
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.moveTaxonomy(context.Background(), nil, moveTaxonomyInput{Kind: "keyword", Item: "Child", Parent: "Parent"})
	if err != nil {
		t.Fatalf("moveTaxonomy: %v", err)
	}
	if movePath != "/api/keyword/3/move/4/" || structuredContentMap(t, res)["parent_id"] != 4.0 {
		t.Fatalf("movePath=%q result=%s", movePath, resultText(t, res))
	}

	h = newHandlersFunc(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be called for item==parent")
	})
	if _, _, err := h.moveTaxonomy(context.Background(), nil, moveTaxonomyInput{Kind: "food", Item: "5", Parent: "5"}); err == nil {
		t.Fatal("expected item==parent rejection")
	}
}

func TestGenericActionPostBodyAndEmptyBody(t *testing.T) {
	var requests []struct {
		method string
		path   string
		body   map[string]any
	}
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			method string
			path   string
			body   map[string]any
		}{method: r.Method, path: r.URL.Path, body: decodeBody(t, r)})
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	res, _, err := h.genericAction(context.Background(), nil, actionInput{
		Method: "post",
		Path:   "ingredient-parser/post/",
		Body:   map[string]any{"ingredient": "2 cups flour"},
		Query:  map[string]string{"locale": "en"},
	})
	if err != nil {
		t.Fatalf("genericAction POST: %v", err)
	}
	if _, ok := structuredContentMap(t, res)["data"]; !ok {
		t.Fatalf("result = %s", resultText(t, res))
	}
	if len(requests) != 1 || requests[0].method != http.MethodPost || requests[0].path != "/api/ingredient-parser/post/" || at(t, requests[0].body, "ingredient") != "2 cups flour" {
		t.Fatalf("requests = %+v", requests)
	}
	_, _, err = h.genericAction(context.Background(), nil, actionInput{
		Method:        "post",
		Path:          "ingredient-parser/post/",
		SendEmptyBody: true,
	})
	if err != nil {
		t.Fatalf("genericAction empty body: %v", err)
	}
	if len(requests) != 2 || len(requests[1].body) != 0 {
		t.Fatalf("empty-body request = %+v", requests)
	}
}
