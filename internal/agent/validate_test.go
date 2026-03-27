package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateToolInput_ValidNoRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	input := json.RawMessage(`{"name":"test"}`)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestValidateToolInput_SchemaNoRequired(t *testing.T) {
	// Schema with no "required" field — any JSON object should pass.
	schema := json.RawMessage(`{"type":"object"}`)
	input := json.RawMessage(`{}`)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error for schema with no required fields, got %v", err)
	}
}

func TestValidateToolInput_RequiredFieldPresent(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`)
	input := json.RawMessage(`{"command":"ls -la"}`)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error when required field is present, got %v", err)
	}
}

func TestValidateToolInput_MissingRequiredField(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`)
	input := json.RawMessage(`{}`)
	err := validateToolInput(input, schema)
	if err == nil {
		t.Fatal("expected error for missing required field, got nil")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention the missing field name; got: %v", err)
	}
}

func TestValidateToolInput_InvalidJSON(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	input := json.RawMessage(`{not valid json}`)
	err := validateToolInput(input, schema)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention 'invalid JSON'; got: %v", err)
	}
}

func TestValidateToolInput_EmptyInputTreatedAsEmptyObject(t *testing.T) {
	// Schema has no required fields — empty input should be valid.
	schema := json.RawMessage(`{"type":"object"}`)
	input := json.RawMessage(``)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error for empty input with no required fields, got %v", err)
	}
}

func TestValidateToolInput_NullInputTreatedAsEmptyObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	input := json.RawMessage(`null`)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error for null input with no required fields, got %v", err)
	}
}

func TestValidateToolInput_EmptySchema(t *testing.T) {
	// Tool with no schema — validation should always pass.
	input := json.RawMessage(`{"anything":"goes"}`)
	if err := validateToolInput(input, nil); err != nil {
		t.Errorf("expected nil error for nil schema, got %v", err)
	}
}

func TestValidateToolInput_MultipleRequiredFields_AllPresent(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["url","method"],"properties":{"url":{"type":"string"},"method":{"type":"string"}}}`)
	input := json.RawMessage(`{"url":"https://example.com","method":"GET"}`)
	if err := validateToolInput(input, schema); err != nil {
		t.Errorf("expected nil error when all required fields present, got %v", err)
	}
}

func TestValidateToolInput_MultipleRequiredFields_OneMissing(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["url","method"],"properties":{"url":{"type":"string"},"method":{"type":"string"}}}`)
	input := json.RawMessage(`{"url":"https://example.com"}`)
	err := validateToolInput(input, schema)
	if err == nil {
		t.Fatal("expected error for missing 'method' field, got nil")
	}
	if !strings.Contains(err.Error(), "method") {
		t.Errorf("error should mention missing field 'method'; got: %v", err)
	}
}
