package setup

import "testing"

func TestModelsForProvider_Anthropic(t *testing.T) {
	result := ModelsForProvider("anthropic")
	if len(result) != 2 {
		t.Fatalf("expected 2 anthropic models, got %d", len(result))
	}
	if result[0].ID != "claude-sonnet-4-6" {
		t.Errorf("result[0].ID = %q, want %q", result[0].ID, "claude-sonnet-4-6")
	}
	if result[0].CostIn != 3.00 {
		t.Errorf("result[0].CostIn = %v, want 3.00", result[0].CostIn)
	}
	if result[0].CostOut != 15.00 {
		t.Errorf("result[0].CostOut = %v, want 15.00", result[0].CostOut)
	}
	if result[0].ContextK != 400 {
		t.Errorf("result[0].ContextK = %d, want 400", result[0].ContextK)
	}
	if result[1].ID != "claude-opus-4-6" {
		t.Errorf("result[1].ID = %q, want %q", result[1].ID, "claude-opus-4-6")
	}
	if result[1].CostIn != 5.00 {
		t.Errorf("result[1].CostIn = %v, want 5.00", result[1].CostIn)
	}
	if result[1].CostOut != 25.00 {
		t.Errorf("result[1].CostOut = %v, want 25.00", result[1].CostOut)
	}
	if result[1].ContextK != 1000 {
		t.Errorf("result[1].ContextK = %d, want 1000", result[1].ContextK)
	}
}

func TestModelsForProvider_Gemini(t *testing.T) {
	result := ModelsForProvider("gemini")
	if len(result) != 2 {
		t.Fatalf("expected 2 gemini models, got %d", len(result))
	}
	if result[0].ID != "gemini-3.1-flash-lite" {
		t.Errorf("result[0].ID = %q, want %q", result[0].ID, "gemini-3.1-flash-lite")
	}
	if result[0].CostIn != 0.25 {
		t.Errorf("result[0].CostIn = %v, want 0.25", result[0].CostIn)
	}
	if result[0].CostOut != 1.50 {
		t.Errorf("result[0].CostOut = %v, want 1.50", result[0].CostOut)
	}
	if result[1].ID != "gemini-3.1-pro" {
		t.Errorf("result[1].ID = %q, want %q", result[1].ID, "gemini-3.1-pro")
	}
	if result[1].CostIn != 2.00 {
		t.Errorf("result[1].CostIn = %v, want 2.00", result[1].CostIn)
	}
	if result[1].CostOut != 12.00 {
		t.Errorf("result[1].CostOut = %v, want 12.00", result[1].CostOut)
	}
	if result[1].ContextK != 200 {
		t.Errorf("result[1].ContextK = %d, want 200", result[1].ContextK)
	}
}

func TestModelsForProvider_OpenAI(t *testing.T) {
	result := ModelsForProvider("openai")
	if len(result) != 2 {
		t.Fatalf("expected 2 openai models, got %d", len(result))
	}
	if result[0].ID != "gpt-5.4" {
		t.Errorf("result[0].ID = %q, want %q", result[0].ID, "gpt-5.4")
	}
	if result[0].CostIn != 2.50 {
		t.Errorf("result[0].CostIn = %v, want 2.50", result[0].CostIn)
	}
	if result[0].CostOut != 15.00 {
		t.Errorf("result[0].CostOut = %v, want 15.00", result[0].CostOut)
	}
	if result[0].ContextK != 1000 {
		t.Errorf("result[0].ContextK = %d, want 1000", result[0].ContextK)
	}
	if result[1].ID != "gpt-5.4-pro" {
		t.Errorf("result[1].ID = %q, want %q", result[1].ID, "gpt-5.4-pro")
	}
	if result[1].CostIn != 30.00 {
		t.Errorf("result[1].CostIn = %v, want 30.00", result[1].CostIn)
	}
	if result[1].CostOut != 180.00 {
		t.Errorf("result[1].CostOut = %v, want 180.00", result[1].CostOut)
	}
}

func TestModelsForProvider_OpenRouter(t *testing.T) {
	result := ModelsForProvider("openrouter")
	if len(result) != 3 {
		t.Fatalf("expected 3 openrouter models, got %d", len(result))
	}
	for i, m := range result {
		if m.CostIn != 0.00 {
			t.Errorf("result[%d].CostIn = %v, want 0.00", i, m.CostIn)
		}
		if m.CostOut != 0.00 {
			t.Errorf("result[%d].CostOut = %v, want 0.00", i, m.CostOut)
		}
	}
}

func TestModelsForProvider_Ollama(t *testing.T) {
	result := ModelsForProvider("ollama")
	if result != nil {
		t.Errorf("ModelsForProvider(\"ollama\") = %v, want nil", result)
	}
}

func TestModelsForProvider_Unknown(t *testing.T) {
	result := ModelsForProvider("unknown-provider")
	if result != nil {
		t.Errorf("ModelsForProvider(\"unknown-provider\") = %v, want nil", result)
	}
}

func TestModelsForProvider_AllFieldsPopulated(t *testing.T) {
	for _, provider := range []string{"anthropic", "gemini", "openai", "openrouter"} {
		result := ModelsForProvider(provider)
		for i, m := range result {
			if m.ID == "" {
				t.Errorf("provider %q model[%d].ID is empty", provider, i)
			}
			if m.DisplayName == "" {
				t.Errorf("provider %q model[%d].DisplayName is empty", provider, i)
			}
			if m.Description == "" {
				t.Errorf("provider %q model[%d].Description is empty", provider, i)
			}
			if m.CostIn < 0 {
				t.Errorf("provider %q model[%d].CostIn = %v is negative", provider, i, m.CostIn)
			}
			if m.CostOut < 0 {
				t.Errorf("provider %q model[%d].CostOut = %v is negative", provider, i, m.CostOut)
			}
		}
	}
}

func TestOtherModelSentinel_NotInCatalog(t *testing.T) {
	if OtherModelSentinel.ID != "" {
		t.Errorf("OtherModelSentinel.ID = %q, want empty string", OtherModelSentinel.ID)
	}
	// Verify no anthropic entry has an empty ID (sentinel should not be in catalog)
	for i, m := range ModelsForProvider("anthropic") {
		if m.ID == "" {
			t.Errorf("anthropic model[%d].ID is empty — sentinel leaked into catalog", i)
		}
	}
}
