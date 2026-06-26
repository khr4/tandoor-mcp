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

type apiRecipe struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Rating      flexNum   `json:"rating"`
	WorkingTime flexNum   `json:"working_time"`
	WaitingTime flexNum   `json:"waiting_time"`
	Servings    flexNum   `json:"servings"`
	SourceURL   string    `json:"source_url"`
	Keywords    []named   `json:"keywords"`
	Steps       []apiStep `json:"steps"`
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
		c.Keywords = append(c.Keywords, k.Name)
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
			names = append(names, k.Name)
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
