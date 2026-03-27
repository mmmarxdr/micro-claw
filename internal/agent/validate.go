package agent

import (
	"encoding/json"
	"fmt"
)

// validateToolInput checks that input is valid JSON and contains all required
// fields declared in the tool's JSON schema. Returns nil if valid.
func validateToolInput(input json.RawMessage, schema json.RawMessage) error {
	// Treat nil/empty input as an empty object.
	raw := string(input)
	if raw == "" || raw == "null" {
		raw = "{}"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if len(schema) == 0 {
		return nil
	}

	var schemaDef map[string]any
	if err := json.Unmarshal(schema, &schemaDef); err != nil {
		// If the schema itself is malformed, skip validation.
		return nil
	}

	// Check required fields from JSON schema.
	required, _ := schemaDef["required"].([]any)
	for _, r := range required {
		field, _ := r.(string)
		if field == "" {
			continue
		}
		if _, ok := args[field]; !ok {
			return fmt.Errorf("missing required field %q", field)
		}
	}

	return nil
}
