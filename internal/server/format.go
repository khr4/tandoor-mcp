package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// listEnvelope is Tandoor's paginated list response.
type listEnvelope struct {
	Count   int             `json:"count"`
	Next    *string         `json:"next"`
	Results json.RawMessage `json:"results"`
}

// named is the minimal {id, name} shape shared by most resources.
type named struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// flexNum tolerates JSON numbers, decimal strings ("2.00") and null, which
// Tandoor uses interchangeably for decimal fields depending on settings. A value
// that is present but not a plain number is kept in Raw and rendered verbatim,
// so an unexpected format is surfaced honestly rather than silently dropped.
type flexNum struct {
	Set   bool    // true when Value holds a parsed number
	Value float64 // parsed numeric value (valid only when Set)
	Raw   string  // original token when present but non-numeric
}

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(b)), `"`))
	if s == "" || s == "null" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		f.Value, f.Set = v, true
		return nil
	}
	f.Raw = s
	return nil
}

// String renders a number without trailing zeros (2, 1.5, 0.25), or the raw
// token if the value was present but not numeric.
func (f flexNum) String() string {
	if f.Set {
		return strconv.FormatFloat(f.Value, 'f', -1, 64)
	}
	return f.Raw
}

// scaleAmounts multiplies every numeric ingredient amount in r by factor, used
// to render a recipe at a different serving count.
func scaleAmounts(r *apiRecipe, factor float64) {
	for si := range r.Steps {
		for ii := range r.Steps[si].Ingredients {
			a := &r.Steps[si].Ingredients[ii].Amount
			if a.Set {
				a.Value *= factor
			}
		}
	}
}

type apiUnit struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PluralName string `json:"plural_name"`
}

type apiFood struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PluralName string `json:"plural_name"`
}

type apiIngredient struct {
	Amount       flexNum  `json:"amount"`
	Unit         *apiUnit `json:"unit"`
	Food         *apiFood `json:"food"`
	Note         string   `json:"note"`
	NoAmount     bool     `json:"no_amount"`
	IsHeader     bool     `json:"is_header"`
	OriginalText string   `json:"original_text"`
}

type apiStep struct {
	Instruction string          `json:"instruction"`
	Time        flexNum         `json:"time"`
	Ingredients []apiIngredient `json:"ingredients"`
}

// apiKeyword captures both name and label because the recipe LIST endpoint
// serializes keywords as {id, label} (no name), while the detail endpoint
// includes name.
type apiKeyword struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

func (k apiKeyword) display() string {
	if k.Name != "" {
		return k.Name
	}
	return k.Label
}

// apiNutrition is the recipe's stored nutrition block (NutritionInformation).
// Values are decimals that Tandoor may serialize as numbers or strings; flexNum
// tolerates both.
type apiNutrition struct {
	Carbohydrates flexNum `json:"carbohydrates"`
	Fats          flexNum `json:"fats"`
	Proteins      flexNum `json:"proteins"`
	Calories      flexNum `json:"calories"`
	Source        string  `json:"source"`
}

// apiProperty is one property value attached to the recipe (e.g. a custom
// nutrition figure), with its type carrying the human-readable name and unit.
type apiProperty struct {
	Amount flexNum         `json:"property_amount"`
	Type   apiPropertyType `json:"property_type"`
}

type apiPropertyType struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type apiRecipe struct {
	ID          int           `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Rating      flexNum       `json:"rating"`
	WorkingTime flexNum       `json:"working_time"`
	WaitingTime flexNum       `json:"waiting_time"`
	Servings    flexNum       `json:"servings"`
	SourceURL   string        `json:"source_url"`
	Keywords    []apiKeyword  `json:"keywords"`
	Steps       []apiStep     `json:"steps"`
	Nutrition   *apiNutrition `json:"nutrition"`
	Properties  []apiProperty `json:"properties"`
}

// recipeCard is the compact result shape returned by find_recipes.
type recipeCard struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Rating         string   `json:"rating,omitempty"`
	WorkingTimeMin string   `json:"working_time_min,omitempty"`
	WaitingTimeMin string   `json:"waiting_time_min,omitempty"`
	Servings       string   `json:"servings,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`
}

// ingredientOut and stepOut mirror the create_recipe / set_recipe_steps input
// shapes (ingredientInput / stepInput) so get_recipe's structured steps can be
// edited and fed straight back without parsing the Markdown.
type ingredientOut struct {
	Amount   *float64 `json:"amount,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Food     string   `json:"food,omitempty"`
	Note     string   `json:"note,omitempty"`
	IsHeader bool     `json:"is_header,omitempty"`
}

type stepOut struct {
	Instruction string          `json:"instruction,omitempty"`
	Time        *int            `json:"time,omitempty"`
	Ingredients []ingredientOut `json:"ingredients"`
}

func toIngredientOut(ing apiIngredient) ingredientOut {
	o := ingredientOut{Note: ing.Note, IsHeader: ing.IsHeader}
	if ing.IsHeader {
		return o // a header carries its text in note
	}
	if !ing.NoAmount && ing.Amount.Set {
		v := ing.Amount.Value
		o.Amount = &v
	}
	if ing.Unit != nil {
		o.Unit = ing.Unit.Name
	}
	if ing.Food != nil {
		o.Food = ing.Food.Name
	}
	return o
}

func toStepOuts(r apiRecipe) []stepOut {
	steps := make([]stepOut, 0, len(r.Steps))
	for _, st := range r.Steps {
		s := stepOut{Instruction: st.Instruction, Ingredients: make([]ingredientOut, 0, len(st.Ingredients))}
		if st.Time.Set && st.Time.Value != 0 {
			t := int(st.Time.Value)
			s.Time = &t
		}
		for _, ing := range st.Ingredients {
			s.Ingredients = append(s.Ingredients, toIngredientOut(ing))
		}
		steps = append(steps, s)
	}
	return steps
}

// nutritionOut and propertyOut are the get_recipe output shapes for the recipe's
// stored nutrition. Empty figures are omitted rather than rendered as "0".
type nutritionOut struct {
	Calories      string `json:"calories,omitempty"`
	Carbohydrates string `json:"carbohydrates,omitempty"`
	Fats          string `json:"fats,omitempty"`
	Proteins      string `json:"proteins,omitempty"`
	Source        string `json:"source,omitempty"`
}

type propertyOut struct {
	Name   string `json:"name"`
	Amount string `json:"amount,omitempty"`
	Unit   string `json:"unit,omitempty"`
}

// toNutrition converts the recipe's nutrition block, returning nil when there is
// nothing to show so the field is omitted entirely.
func toNutrition(r apiRecipe) *nutritionOut {
	n := r.Nutrition
	if n == nil {
		return nil
	}
	out := nutritionOut{
		Calories:      n.Calories.String(),
		Carbohydrates: n.Carbohydrates.String(),
		Fats:          n.Fats.String(),
		Proteins:      n.Proteins.String(),
		Source:        strings.TrimSpace(n.Source),
	}
	if out == (nutritionOut{}) {
		return nil
	}
	return &out
}

// toProperties converts the recipe's attached property values, keeping only those
// with a named type.
func toProperties(r apiRecipe) []propertyOut {
	var out []propertyOut
	for _, p := range r.Properties {
		name := strings.TrimSpace(p.Type.Name)
		if name == "" {
			continue
		}
		out = append(out, propertyOut{Name: name, Amount: p.Amount.String(), Unit: strings.TrimSpace(p.Type.Unit)})
	}
	return out
}

func toCard(r apiRecipe) recipeCard {
	c := recipeCard{
		ID:             r.ID,
		Name:           r.Name,
		Rating:         r.Rating.String(),
		WorkingTimeMin: r.WorkingTime.String(),
		WaitingTimeMin: r.WaitingTime.String(),
		Servings:       r.Servings.String(),
	}
	for _, k := range r.Keywords {
		c.Keywords = append(c.Keywords, k.display())
	}
	return c
}

// formatIngredient renders one ingredient as a readable line, e.g. "2 cup flour"
// or "salt (to taste)".
func formatIngredient(ing apiIngredient) string {
	if ing.IsHeader {
		return strings.TrimSpace(ing.Note)
	}
	var parts []string
	if !ing.NoAmount {
		if a := ing.Amount.String(); a != "" {
			parts = append(parts, a)
		}
	}
	if ing.Unit != nil && ing.Unit.Name != "" {
		parts = append(parts, ing.Unit.Name)
	}
	if ing.Food != nil && ing.Food.Name != "" {
		parts = append(parts, ing.Food.Name)
	}
	line := strings.Join(parts, " ")
	if line == "" {
		line = strings.TrimSpace(ing.OriginalText)
	}
	if ing.Note != "" {
		line = strings.TrimSpace(line + " (" + ing.Note + ")")
	}
	return line
}

// renderRecipe produces a compact, readable Markdown view of a full recipe.
func renderRecipe(r apiRecipe) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (id %d)", r.Name, r.ID)
	if s := r.Rating.String(); s != "" {
		fmt.Fprintf(&b, "  ★%s", s)
	}
	b.WriteString("\n")

	var meta []string
	if s := r.Servings.String(); s != "" {
		meta = append(meta, "serves "+s)
	}
	if s := r.WorkingTime.String(); s != "" && s != "0" {
		meta = append(meta, "prep "+s+" min")
	}
	if s := r.WaitingTime.String(); s != "" && s != "0" {
		meta = append(meta, "cook "+s+" min")
	}
	if len(meta) > 0 {
		fmt.Fprintf(&b, "%s\n", strings.Join(meta, " · "))
	}
	if len(r.Keywords) > 0 {
		names := make([]string, 0, len(r.Keywords))
		for _, k := range r.Keywords {
			names = append(names, k.display())
		}
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(names, ", "))
	}
	if r.SourceURL != "" {
		fmt.Fprintf(&b, "Source: %s\n", r.SourceURL)
	}
	if d := strings.TrimSpace(r.Description); d != "" {
		fmt.Fprintf(&b, "\n%s\n", d)
	}

	for i, st := range r.Steps {
		fmt.Fprintf(&b, "\n## Step %d", i+1)
		if t := st.Time.String(); t != "" && t != "0" {
			fmt.Fprintf(&b, " (%s min)", t)
		}
		b.WriteString("\n")
		for _, ing := range st.Ingredients {
			if line := formatIngredient(ing); line != "" {
				fmt.Fprintf(&b, "- %s\n", line)
			}
		}
		if instr := strings.TrimSpace(st.Instruction); instr != "" {
			fmt.Fprintf(&b, "\n%s\n", instr)
		}
	}
	return b.String()
}
