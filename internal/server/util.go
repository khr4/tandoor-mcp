package server

import "encoding/json"

// decodeList unmarshals a Tandoor list response into v (a pointer to a slice),
// accepting either a bare JSON array or a paginated {results:[...]} envelope.
func decodeList(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err == nil {
		return nil
	}
	var env listEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	return json.Unmarshal(env.Results, v)
}
