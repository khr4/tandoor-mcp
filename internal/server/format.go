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
// Tandoor uses interchangeably for decimal fields depending on settings.
type flexNum struct {
	Set   bool
	Value float64
}

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // tolerate unexpected formats rather than failing a whole decode
	}
	f.Value, f.Set = v, true
	return nil
}

// String renders a number without trailing zeros: 2, 1.5, 0.25.
func (f flexNum) String() string {
	if !f.Set {
		return ""
	}
	return strconv.FormatFloat(f.Value, 'f', -1, 64)
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
