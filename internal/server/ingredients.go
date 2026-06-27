package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ingredientInput accepts an ingredient either as a natural-language line
// ("2 cups flour", "salt to taste") or as explicit fields. Quantities are
// always captured separately: a parsed line is split into amount + unit + food,
// and the structured form keeps amount and unit distinct from the food.
type ingredientInput struct {
	Text     string   `json:"text,omitempty" jsonschema:"natural ingredient line, e.g. \"2 cups flour\" or \"salt to taste\"; parsed into amount, unit and food automatically"`
	Amount   *float64 `json:"amount,omitempty" jsonschema:"numeric quantity (use with unit and food instead of text)"`
	Unit     string   `json:"unit,omitempty" jsonschema:"unit name for the amount, e.g. g, ml, cup, tbsp; leave empty for countable items like eggs"`
	Food     string   `json:"food,omitempty" jsonschema:"food/ingredient name, e.g. flour"`
	Note     string   `json:"note,omitempty" jsonschema:"preparation note, e.g. finely chopped"`
	IsHeader bool     `json:"is_header,omitempty" jsonschema:"true for a section header line (e.g. \"For the sauce\"); note or text must contain the header text"`
}

type stepInput struct {
	Instruction string            `json:"instruction,omitempty" jsonschema:"step instructions (Markdown allowed)"`
	Time        *int              `json:"time,omitempty" jsonschema:"time for this step in minutes"`
	Ingredients []ingredientInput `json:"ingredients,omitempty" jsonschema:"ingredients used in this step"`
}

// parseIngredientLines turns natural-language lines into structured ingredients
// using Tandoor's ingredient parser. Results are in input order.
//
// The endpoint is the viewset's POST action, whose route is ".../post/" (the
// action method is named "post", so DRF derives that url_path). Posting to the
// bare "ingredient-parser/" instead falls through to the SPA catch-all and is
// rejected by CSRF — verified against a live instance.
func (h *handlers) parseIngredientLines(ctx context.Context, lines []string) ([]apiIngredient, error) {
	raw, err := h.c.Do(ctx, http.MethodPost, "ingredient-parser/post/", nil, map[string]any{"ingredients": lines})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Ingredients []apiIngredient `json:"ingredients"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decoding parsed ingredients: %w", err)
	}
	return resp.Ingredients, nil
}

// buildSteps converts step inputs into the nested payload Tandoor's recipe
// serializer expects, parsing any natural-language ingredient lines in a single
// batch call. Foods and units are emitted by name; Tandoor get-or-creates them.
func (h *handlers) buildSteps(ctx context.Context, steps []stepInput) ([]map[string]any, error) {
	type ref struct{ s, i int }
	var (
		texts []string
		refs  []ref
	)
	for si, st := range steps {
		for ii, ing := range st.Ingredients {
			// A header is emitted verbatim; never send its text to the parser.
			if !ing.IsHeader && strings.TrimSpace(ing.Text) != "" {
				texts = append(texts, ing.Text)
				refs = append(refs, ref{si, ii})
			}
		}
	}

	parsed := map[ref]apiIngredient{}
	if len(texts) > 0 {
		results, err := h.parseIngredientLines(ctx, texts)
		if err != nil {
			return nil, err
		}
		// The parser returns exactly one result per non-blank line, in order.
		// A mismatch would silently mis-pair quantities, so fail loudly instead.
		if len(results) != len(texts) {
			return nil, fmt.Errorf("ingredient parser returned %d results for %d lines", len(results), len(texts))
		}
		for k, r := range refs {
			parsed[r] = results[k]
		}
	}

	out := make([]map[string]any, 0, len(steps))
	for si, st := range steps {
		ings := make([]map[string]any, 0, len(st.Ingredients))
		for ii, ing := range st.Ingredients {
			var p *apiIngredient
			if pv, ok := parsed[ref{si, ii}]; ok {
				p = &pv
			}
			payload, err := buildIngredient(ing, p)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", si+1, err)
			}
			if payload != nil {
				ings = append(ings, payload)
			}
		}
		out = append(out, buildStep(st.Instruction, si, st.Time, ings))
	}
	return out, nil
}

// buildStep assembles one step payload. Shared by recipe creation and import so
// the nested shape lives in exactly one place.
func buildStep(instruction string, order int, minutes *int, ingredients []map[string]any) map[string]any {
	step := map[string]any{
		"instruction":            instruction,
		"ingredients":            ingredients,
		"show_ingredients_table": true,
		"order":                  order,
	}
	if minutes != nil {
		step["time"] = *minutes
	}
	return step
}

// buildIngredient produces one nested ingredient payload. parsed is non-nil when
// the input was a natural-language line.
func buildIngredient(in ingredientInput, parsed *apiIngredient) (map[string]any, error) {
	// A section header is checked first, before the parser branch, so that
	// {is_header:true, text:"For the sauce"} is never run through the ingredient
	// parser (which would create a phantom food). The header text may arrive in
	// note or text.
	if in.IsHeader {
		text := strings.TrimSpace(in.Note)
		if text == "" {
			text = strings.TrimSpace(in.Text)
		}
		if text == "" {
			return nil, fmt.Errorf("ingredient header needs note or text")
		}
		return map[string]any{
			"is_header":     true,
			"no_amount":     true,
			"note":          text,
			"food":          nil,
			"unit":          nil,
			"amount":        0,
			"original_text": text,
		}, nil
	}

	// A line is either a natural-language text line or structured fields, never
	// both; refuse the mix rather than silently dropping the structured side.
	if strings.TrimSpace(in.Text) != "" && (strings.TrimSpace(in.Food) != "" || in.Amount != nil || strings.TrimSpace(in.Unit) != "") {
		return nil, fmt.Errorf("ingredient %q: provide a natural-language text line OR structured amount/unit/food, not both", strings.TrimSpace(in.Text))
	}

	if parsed != nil {
		m := map[string]any{
			"note":          parsed.Note,
			"no_amount":     parsed.NoAmount,
			"original_text": strings.TrimSpace(in.Text),
			"unit":          nil,
			"food":          nil,
		}
		if parsed.Amount.Set && parsed.Amount.Value != 0 {
			m["amount"] = parsed.Amount.Value
		} else {
			// The parser returns a numeric 0 (not a Raw token) for a line with no
			// number, e.g. "salt to taste". Treat absent / zero / non-numeric all
			// as no-amount so it never renders "0 salt".
			m["amount"] = 0
			m["no_amount"] = true
		}
		if parsed.Unit != nil && parsed.Unit.Name != "" {
			m["unit"] = map[string]any{"name": parsed.Unit.Name}
		}
		if parsed.Food != nil && parsed.Food.Name != "" {
			m["food"] = map[string]any{"name": parsed.Food.Name}
		}
		return m, nil
	}

	food := strings.TrimSpace(in.Food)
	if food == "" {
		return nil, fmt.Errorf("ingredient needs either text or a food name")
	}
	// No number given means no amount, even if a unit (e.g. "pinch") is present —
	// otherwise the ingredient would render as "0 pinch salt".
	noAmount := in.Amount == nil
	m := map[string]any{
		"food":          map[string]any{"name": food},
		"note":          in.Note,
		"no_amount":     noAmount,
		"unit":          nil,
		"original_text": synthOriginal(in),
	}
	if in.Amount != nil {
		m["amount"] = *in.Amount
	} else {
		m["amount"] = 0
	}
	if u := strings.TrimSpace(in.Unit); u != "" {
		m["unit"] = map[string]any{"name": u}
	}
	return m, nil
}

// synthOriginal reconstructs a display string for a structured ingredient.
func synthOriginal(in ingredientInput) string {
	var parts []string
	if in.Amount != nil {
		parts = append(parts, strconv.FormatFloat(*in.Amount, 'f', -1, 64))
	}
	if u := strings.TrimSpace(in.Unit); u != "" {
		parts = append(parts, u)
	}
	if f := strings.TrimSpace(in.Food); f != "" {
		parts = append(parts, f)
	}
	line := strings.Join(parts, " ")
	if n := strings.TrimSpace(in.Note); n != "" {
		line = strings.TrimSpace(line + " (" + n + ")")
	}
	return line
}
