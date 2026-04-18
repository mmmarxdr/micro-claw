package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNextStep_CLISkipsChannelExtra(t *testing.T) {
	got := nextStep(stepChannel, "cli")
	if got != stepStorePath {
		t.Errorf("nextStep(stepChannel, cli) = %v, want %v (stepStorePath)", got, stepStorePath)
	}
}

func TestNextStep_TelegramIncludesChannelExtra(t *testing.T) {
	got := nextStep(stepChannel, "telegram")
	if got != stepChannelExtra {
		t.Errorf("nextStep(stepChannel, telegram) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestNextStep_DiscordIncludesChannelExtra(t *testing.T) {
	got := nextStep(stepChannel, "discord")
	if got != stepChannelExtra {
		t.Errorf("nextStep(stepChannel, discord) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestPrevStep_StorePathWithCLI(t *testing.T) {
	got := prevStep(stepStorePath, "cli")
	if got != stepChannel {
		t.Errorf("prevStep(stepStorePath, cli) = %v, want %v (stepChannel)", got, stepChannel)
	}
}

func TestPrevStep_StorePathWithTelegram(t *testing.T) {
	got := prevStep(stepStorePath, "telegram")
	if got != stepChannelExtra {
		t.Errorf("prevStep(stepStorePath, telegram) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestDefaultModelForProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"anthropic", "claude-sonnet-4-6"},
		{"gemini", "gemini-3.1-flash-lite"},
		{"openai", "gpt-5.4"},
		{"ollama", "llama3.2"},
		{"openrouter", "openrouter/free"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := defaultModelForProvider(tc.provider)
			if got != tc.want {
				t.Errorf("defaultModelForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}

func TestNextStep_ChannelExtraToStorePath(t *testing.T) {
	got := nextStep(stepChannelExtra, "telegram")
	if got != stepStorePath {
		t.Errorf("nextStep(stepChannelExtra, telegram) = %v, want %v", got, stepStorePath)
	}
}

func TestPrevStep_ClampsAtZero(t *testing.T) {
	got := prevStep(stepProvider, "cli")
	if got != stepProvider {
		t.Errorf("prevStep(stepProvider, cli) = %v, want %v (stepProvider/0)", got, stepProvider)
	}
}

func TestPrevStep_StorePathWithDiscord(t *testing.T) {
	got := prevStep(stepStorePath, "discord")
	if got != stepChannelExtra {
		t.Errorf("prevStep(stepStorePath, discord) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestNextStep_WhatsAppIncludesChannelExtra(t *testing.T) {
	got := nextStep(stepChannel, "whatsapp")
	if got != stepChannelExtra {
		t.Errorf("nextStep(stepChannel, whatsapp) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestPrevStep_StorePathWithWhatsApp(t *testing.T) {
	got := prevStep(stepStorePath, "whatsapp")
	if got != stepChannelExtra {
		t.Errorf("prevStep(stepStorePath, whatsapp) = %v, want %v (stepChannelExtra)", got, stepChannelExtra)
	}
}

func TestSelectorModel_Selected(t *testing.T) {
	s := selectorModel{choices: []string{"a", "b", "c"}, cursor: 1}
	if got := s.Selected(); got != "b" {
		t.Errorf("Selected() = %q, want %q", got, "b")
	}
}

func TestSelectorModel_EmptyChoices(t *testing.T) {
	s := selectorModel{}
	if got := s.Selected(); got != "" {
		t.Errorf("Selected() on empty selectorModel = %q, want %q", got, "")
	}
}

func TestBuildConfig_AllowedUsersParsed(t *testing.T) {
	m := newWizardModel()

	// Simulate user selecting "telegram" channel
	m.channelSelector = selectorModel{
		choices: []string{"cli", "telegram", "discord"},
		cursor:  1, // telegram
	}

	// Set token and allowed users inputs
	tokenInput := textinput.New()
	tokenInput.SetValue("bot-token-123")
	m.tokenInput = tokenInput

	allowedUsersInput := textinput.New()
	allowedUsersInput.SetValue("123456, 789012, bad_token")
	m.allowedUsersInput = allowedUsersInput

	cfg := m.buildConfig()

	want := []int64{123456, 789012}
	if !reflect.DeepEqual(cfg.Channel.AllowedUsers, want) {
		t.Errorf("AllowedUsers = %v, want %v", cfg.Channel.AllowedUsers, want)
	}
}

func TestBuildConfig_AllowedUsersEmptyInput(t *testing.T) {
	m := newWizardModel()
	m.channelSelector = selectorModel{
		choices: []string{"cli", "telegram", "discord"},
		cursor:  1, // telegram
	}
	// allowedUsersInput left empty
	cfg := m.buildConfig()
	if len(cfg.Channel.AllowedUsers) != 0 {
		t.Errorf("expected empty AllowedUsers for empty input, got %v", cfg.Channel.AllowedUsers)
	}
}

func TestBuildConfig_AllowedUsersNotSetForCLI(t *testing.T) {
	m := newWizardModel()
	// default channelSelector cursor=0 which is "cli"

	allowedUsersInput := textinput.New()
	allowedUsersInput.SetValue("123456,789012")
	m.allowedUsersInput = allowedUsersInput

	cfg := m.buildConfig()
	if len(cfg.Channel.AllowedUsers) != 0 {
		t.Errorf("AllowedUsers should not be set for CLI channel, got %v", cfg.Channel.AllowedUsers)
	}
}

// ---------------------------------------------------------------------------
// Phase 9 — modelSelectorModel integration tests
// ---------------------------------------------------------------------------

func TestModelSelectorModel_ListMode_DownMovescursor(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	// starts at cursor 0
	updated, _ := ms.Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated.cursor != 1 {
		t.Errorf("cursor after Down = %d, want 1", updated.cursor)
	}
}

func TestModelSelectorModel_ListMode_DownDoesNotExceedLastRow(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	// anthropic has 2 real models + OtherModelSentinel = 3 rows; last index = 2
	ms.cursor = len(ms.items) - 1
	updated, _ := ms.Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated.cursor != len(ms.items)-1 {
		t.Errorf("cursor after Down at last row = %d, want %d", updated.cursor, len(ms.items)-1)
	}
}

func TestModelSelectorModel_ListMode_UpMovescursor(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	ms.cursor = 1
	updated, _ := ms.Update(tea.KeyMsg{Type: tea.KeyUp})
	if updated.cursor != 0 {
		t.Errorf("cursor after Up = %d, want 0", updated.cursor)
	}
}

func TestModelSelectorModel_ListMode_UpDoesNotGoBelowZero(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	ms.cursor = 0
	updated, _ := ms.Update(tea.KeyMsg{Type: tea.KeyUp})
	if updated.cursor != 0 {
		t.Errorf("cursor after Up at row 0 = %d, want 0", updated.cursor)
	}
}

func TestModelSelectorModel_EnterOnOther_SwitchesToFreeText(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	// Navigate to last row (OtherModelSentinel)
	ms.cursor = len(ms.items) - 1
	updated, shouldAdvance := ms.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.freeTextMode {
		t.Error("freeTextMode should be true after selecting Other...")
	}
	if shouldAdvance {
		t.Error("shouldAdvance should be false when selecting Other...")
	}
}

func TestModelSelectorModel_EnterOnModel_SignalsAdvance(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	ms.cursor = 0 // first real model
	_, shouldAdvance := ms.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !shouldAdvance {
		t.Error("shouldAdvance should be true when Enter on a real model")
	}
}

func TestModelSelectorModel_EscInFreeText_ReturnToList(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	// Navigate to Other... and select it to enter freeTextMode
	ms.cursor = len(ms.items) - 1
	ms, _ = ms.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !ms.freeTextMode {
		t.Fatal("precondition: should be in freeTextMode after selecting Other...")
	}
	// Now press Esc
	updated, _ := ms.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.freeTextMode {
		t.Error("freeTextMode should be false after Esc in free-text mode")
	}
}

func TestModelSelectorModel_SelectedModelID_ListMode(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	ms.cursor = 0
	got := ms.SelectedModelID()
	if got != "claude-sonnet-4-6" {
		t.Errorf("SelectedModelID() = %q, want %q", got, "claude-sonnet-4-6")
	}
}

func TestModelSelectorModel_SelectedModelID_FreeText(t *testing.T) {
	ms := newModelSelectorModel("anthropic", 80)
	ms.freeTextMode = true
	ms.textInput.SetValue("my-custom-model")
	got := ms.SelectedModelID()
	if got != "my-custom-model" {
		t.Errorf("SelectedModelID() = %q, want %q", got, "my-custom-model")
	}
}

func TestModelSelectorModel_Ollama_StartsInFreeText(t *testing.T) {
	ms := newModelSelectorModel("ollama", 80)
	if !ms.freeTextMode {
		t.Error("ollama selector should start in freeTextMode")
	}
	if len(ms.items) != 0 {
		t.Errorf("ollama selector items should be nil/empty, got %d items", len(ms.items))
	}
}

func TestBuildConfig_UsesModelSelectorValue(t *testing.T) {
	m := newWizardModel()
	// Set provider to anthropic (default)
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	// Reinitialize selector for anthropic and move cursor to index 1 (claude-opus-4-6)
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	m.modelSelector.cursor = 1
	cfg := m.buildConfig()
	if cfg.Models.Default.Model != "claude-opus-4-6" {
		t.Errorf("buildConfig().Models.Default.Model = %q, want %q", cfg.Models.Default.Model, "claude-opus-4-6")
	}
}

func TestAdvance_CredentialsOllama_SkipsAPIKeyCheck(t *testing.T) {
	m := newWizardModel()
	// Set step to stepCredentials
	m.step = stepCredentials
	// Set provider to ollama
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  4, // ollama
	}
	// Initialize selector for ollama with a non-empty free-text value
	m.modelSelector = newModelSelectorModel("ollama", 80)
	m.modelSelector.textInput.SetValue("llama3.2")
	// apiKeyInput is left empty (zero-value)

	model, _ := m.advance()
	wm := model.(WizardModel)
	// Should have advanced to stepChannel (next step after stepCredentials for cli channel)
	if wm.step == stepCredentials {
		t.Error("advance() should have moved past stepCredentials for ollama with empty api key")
	}
}

func TestAdvance_CredentialsNonOllama_RequiresAPIKey(t *testing.T) {
	m := newWizardModel()
	m.step = stepCredentials
	// anthropic with valid model selection but no api key
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	m.modelSelector.cursor = 0 // claude-sonnet-4-6
	// apiKeyInput is empty

	model, _ := m.advance()
	wm := model.(WizardModel)
	// With warning-only validation, advance should proceed
	if wm.step != stepChannel {
		t.Error("advance() should advance with warning when apiKey is empty for non-ollama provider")
	}
	// Should have warning
	if !strings.Contains(wm.warningMsg, "API key is empty") {
		t.Error("should have warning about empty API key")
	}
}

func TestYAMLPreview_RedactsAPIKey(t *testing.T) {
	m := newWizardModel()
	m.step = stepCredentials
	// Set up anthropic with a real api key
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	m.modelSelector.cursor = 0
	apiKeyInput := textinput.New()
	apiKeyInput.SetValue("sk-real-secret")
	m.apiKeyInput = apiKeyInput

	// Advance through credentials and other steps to reach stepConfirm
	// We'll directly simulate the advance logic by setting step to just before confirm
	m.step = stepStorePath
	m.storePathInput.SetValue("~/.microagent/data")
	// Advance to stepConfirm
	model, _ := m.advance()
	wm := model.(WizardModel)

	if wm.step != stepConfirm {
		t.Fatalf("expected stepConfirm, got step %d", wm.step)
	}
	if strings.Contains(wm.yamlPreview, "sk-real-secret") {
		t.Error("yamlPreview should not contain the real api key")
	}
	if !strings.Contains(wm.yamlPreview, "***") {
		t.Error("yamlPreview should contain '***' redaction for api key")
	}
}

func TestWizard_DetectsLocalConfigYAML(t *testing.T) {
	// Create a temporary directory with a config.yaml
	tmpDir := t.TempDir()
	localConfigPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(localConfigPath, []byte("# existing config"), 0644); err != nil {
		t.Fatalf("write local config.yaml: %v", err)
	}

	// Save original working directory and change to temp dir
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(originalWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}

	// Create a wizard model and advance to stepConfirm
	m := newWizardModel()
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	m.modelSelector.cursor = 0
	apiKeyInput := textinput.New()
	apiKeyInput.SetValue("sk-test")
	m.apiKeyInput = apiKeyInput
	m.channelSelector = selectorModel{
		choices: []string{"cli", "telegram", "discord"},
		cursor:  0, // cli
	}
	m.storePathInput.SetValue("~/.microagent/data")
	m.step = stepStorePath

	// Advance to stepConfirm
	model, _ := m.advance()
	wm := model.(WizardModel)

	if wm.step != stepConfirm {
		t.Fatalf("expected stepConfirm, got step %d", wm.step)
	}

	// Check that the wizard detected local config.yaml
	// The view should mention local config.yaml
	view := wm.View()
	if !strings.Contains(view, "config.yaml") {
		t.Error("wizard view should mention config.yaml when local file exists")
	}

	// The view should show local path, not ~/.microagent/config.yaml
	if strings.Contains(view, "~/.microagent/config.yaml") {
		t.Error("wizard should not show default path when local config.yaml exists")
	}
}

func TestYAMLPreview_RedactsToken(t *testing.T) {
	m := newWizardModel()
	// Set channel to telegram with a real token
	m.channelSelector = selectorModel{
		choices: []string{"cli", "telegram", "discord"},
		cursor:  1, // telegram
	}
	tokenInput := textinput.New()
	tokenInput.SetValue("bot123:realtoken")
	m.tokenInput = tokenInput
	allowedInput := textinput.New()
	allowedInput.SetValue("12345")
	m.allowedUsersInput = allowedInput

	// Advance to stepConfirm by simulating the step sequence
	m.step = stepStorePath
	m.storePathInput.SetValue("~/.microagent/data")
	model, _ := m.advance()
	wm := model.(WizardModel)

	if wm.step != stepConfirm {
		t.Fatalf("expected stepConfirm, got step %d", wm.step)
	}
	if strings.Contains(wm.yamlPreview, "bot123:realtoken") {
		t.Error("yamlPreview should not contain the real bot token")
	}
}

func TestYAMLPreview_EmptyAPIKeyNotRedacted_Ollama(t *testing.T) {
	m := newWizardModel()
	// Ollama provider with empty api key
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  4, // ollama
	}
	m.modelSelector = newModelSelectorModel("ollama", 80)
	m.modelSelector.textInput.SetValue("llama3.2")
	// apiKeyInput is empty

	m.step = stepStorePath
	m.storePathInput.SetValue("~/.microagent/data")
	model, _ := m.advance()
	wm := model.(WizardModel)

	if wm.step != stepConfirm {
		t.Fatalf("expected stepConfirm, got step %d", wm.step)
	}
	// Empty api key should NOT be redacted as "***"
	// (we only redact non-empty values)
	if strings.Contains(wm.yamlPreview, "api_key: '***'") || strings.Contains(wm.yamlPreview, "api_key: \"***\"") {
		t.Error("empty api_key should not be redacted to '***' for ollama")
	}
}

// ---------------------------------------------------------------------------
// Phase 10 — handleKey Enter on catalog model tests
// ---------------------------------------------------------------------------

// TestHandleKey_EnterOnCatalogModel_ShiftsFocusToAPIKey verifies that pressing
// Enter on a real catalog model (non-ollama, list mode) does NOT advance the
// step but instead shifts focus to the API key input.
func TestHandleKey_EnterOnCatalogModel_ShiftsFocusToAPIKey(t *testing.T) {
	m := newWizardModel()
	m.step = stepCredentials
	// anthropic provider, cursor on first real model (not OtherModelSentinel)
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	m.modelSelector.cursor = 0 // claude-sonnet-4-6 (real model)
	m.focusSelectorActive = true

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	wm := result.(WizardModel)

	if wm.step != stepCredentials {
		t.Errorf("step should still be stepCredentials, got %d", wm.step)
	}
	if wm.focusSelectorActive {
		t.Error("focusSelectorActive should be false after Enter on catalog model")
	}
	if !wm.apiKeyInput.Focused() {
		t.Error("apiKeyInput should be focused after Enter on catalog model")
	}
}

// TestHandleKey_EnterOnCatalogModel_OllamaAdvances verifies that pressing Enter
// on a catalog model when provider is ollama advances past stepCredentials directly
// (ollama has no API key requirement).
func TestHandleKey_EnterOnCatalogModel_OllamaAdvances(t *testing.T) {
	m := newWizardModel()
	m.step = stepCredentials
	// ollama provider — starts in freeTextMode, so we set up a hypothetical
	// list-mode ollama by using a provider that has a catalog but simulating
	// the ollama path: we test via advance() path which is already covered,
	// so here we verify that ollama in freeTextMode with a model value advances.
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  4, // ollama
	}
	m.modelSelector = newModelSelectorModel("ollama", 80)
	m.modelSelector.textInput.SetValue("llama3.2")
	m.focusSelectorActive = true

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	wm := result.(WizardModel)

	if wm.step == stepCredentials {
		t.Error("advance() should have moved past stepCredentials for ollama with a model set")
	}
}

// TestAdvance_ChannelExtra_AllowedUsersOptional verifies that advance() succeeds
// at stepChannelExtra when only the token is filled (allowedUsers is empty).
func TestAdvance_ChannelExtra_AllowedUsersOptional(t *testing.T) {
	m := newWizardModel()
	m.step = stepChannelExtra
	m.channelSelector = selectorModel{
		choices: []string{"cli", "telegram", "discord"},
		cursor:  1, // telegram
	}

	tokenInput := textinput.New()
	tokenInput.SetValue("bot-token-123")
	m.tokenInput = tokenInput
	// allowedUsersInput left empty

	m.storePathInput.SetValue("~/.microagent/data") // needed to not block next step

	result, _ := m.advance()
	wm := result.(WizardModel)
	if wm.step == stepChannelExtra {
		t.Error("advance() should have moved past stepChannelExtra when only token is provided (allowedUsers is optional)")
	}
	if wm.validationErr != "" {
		t.Errorf("validationErr should be empty when advance succeeds, got %q", wm.validationErr)
	}
}

// TestHandleKey_APIKeyPasteRoutedToInput verifies that a tea.KeyRunes message
// reaches apiKeyInput when focusSelectorActive=false in list mode (BUG-3 regression).
func TestHandleKey_APIKeyPasteRoutedToInput(t *testing.T) {
	m := newWizardModel()
	m.step = stepCredentials
	m.providerSelector = selectorModel{
		choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		cursor:  0, // anthropic
	}
	m.modelSelector = newModelSelectorModel("anthropic", 80)
	// List mode (freeTextMode==false), API key has focus
	m.focusSelectorActive = false
	m = m.focusCurrentStep()

	// Simulate typing a character into the API key field
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	wm := result.(WizardModel)

	if wm.apiKeyInput.Value() != "s" {
		t.Errorf("apiKeyInput.Value() = %q, want %q — KeyRunes not routed to apiKeyInput in list mode", wm.apiKeyInput.Value(), "s")
	}
}

// TestAdvance_ShowsValidationError verifies that validationErr is set when
// advance() is blocked by missing required input (BUG-12 regression).
func TestAdvance_ShowsValidationError(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(m *WizardModel)
		wantErr string
	}{
		{
			name: "credentials: missing model",
			setup: func(m *WizardModel) {
				m.step = stepCredentials
				m.providerSelector = selectorModel{
					choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
					cursor:  0, // anthropic
				}
				// Position cursor on OtherModelSentinel so IsReadyToAdvance() == false
				ms := newModelSelectorModel("anthropic", 80)
				ms.cursor = len(ms.items) - 1 // OtherModelSentinel
				m.modelSelector = ms
			},
			wantErr: "Please select or enter a model",
		},
		{
			name: "credentials: missing api key",
			setup: func(m *WizardModel) {
				m.step = stepCredentials
				m.providerSelector = selectorModel{
					choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
					cursor:  0, // anthropic
				}
				m.modelSelector = newModelSelectorModel("anthropic", 80)
				m.modelSelector.cursor = 0 // valid model; apiKeyInput left empty
			},
			wantErr: "API key is required",
		},
		{
			name: "channelExtra: missing token",
			setup: func(m *WizardModel) {
				m.step = stepChannelExtra
				// tokenInput left empty
			},
			wantErr: "Bot token is required",
		},
		{
			name: "storePath: missing path",
			setup: func(m *WizardModel) {
				m.step = stepStorePath
				m.storePathInput.SetValue("")
			},
			wantErr: "Store path is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newWizardModel()
			tc.setup(&m)
			result, _ := m.advance()
			wm := result.(WizardModel)

			// Special handling for API key warning (non-blocking warning instead of error)
			if tc.name == "credentials: missing api key" {
				// Should have warning, not validation error
				if wm.validationErr != "" {
					t.Errorf("validationErr should be empty for warning case, got %q", wm.validationErr)
				}
				if !strings.Contains(wm.warningMsg, "API key is empty") {
					t.Errorf("warningMsg = %q, should contain 'API key is empty'", wm.warningMsg)
				}
			} else {
				// All other cases: validation error should be set
				if wm.validationErr != tc.wantErr {
					t.Errorf("validationErr = %q, want %q", wm.validationErr, tc.wantErr)
				}
			}
		})
	}
}

// TestWizardWarnsWhenAPIKeyEmptyForNonOllama tests that the wizard shows a warning
// (not an error) when API key is empty for non-ollama providers, but still allows advance.
func TestWizardWarnsWhenAPIKeyEmptyForNonOllama(t *testing.T) {
	// Test for anthropic (non-ollama)
	t.Run("anthropic with empty API key shows warning", func(t *testing.T) {
		m := newWizardModel()
		m.step = stepCredentials
		m.providerSelector = selectorModel{
			choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
			cursor:  0, // anthropic
		}
		m.modelSelector = newModelSelectorModel("anthropic", 80)
		m.modelSelector.cursor = 0 // valid model
		m.apiKeyInput.SetValue("") // empty API key

		// Check that warning is shown in the view when on credentials step
		view := m.View()
		if !strings.Contains(view, "API key is empty") {
			t.Error("View should show API key warning when API key is empty")
		}

		// Should advance without blocking
		result, _ := m.advance()
		wm := result.(WizardModel)

		// Should have no validation error (not blocked)
		if wm.validationErr != "" {
			t.Errorf("validationErr should be empty for warning case, got %q", wm.validationErr)
		}

		// Should have advanced to next step
		if wm.step != stepChannel {
			t.Errorf("step = %v, want %v (should advance despite warning)", wm.step, stepChannel)
		}
	})

	// Test for ollama (should have no warning even with empty API key)
	t.Run("ollama with empty API key has no warning", func(t *testing.T) {
		m := newWizardModel()
		m.step = stepCredentials
		m.providerSelector = selectorModel{
			choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
			cursor:  4, // ollama
		}
		m.modelSelector = newModelSelectorModel("ollama", 80)
		m.modelSelector.cursor = 0 // valid model
		m.apiKeyInput.SetValue("") // empty API key (allowed for ollama)

		result, _ := m.advance()
		wm := result.(WizardModel)

		// Should have no validation error
		if wm.validationErr != "" {
			t.Errorf("validationErr should be empty for ollama, got %q", wm.validationErr)
		}

		// Should have no warning
		if wm.warningMsg != "" {
			t.Errorf("warningMsg should be empty for ollama, got %q", wm.warningMsg)
		}

		// Should have advanced to next step
		if wm.step != stepChannel {
			t.Errorf("step = %v, want %v", wm.step, stepChannel)
		}
	})

	// Test for non-ollama with non-empty API key (should have no warning)
	t.Run("anthropic with non-empty API key has no warning", func(t *testing.T) {
		m := newWizardModel()
		m.step = stepCredentials
		m.providerSelector = selectorModel{
			choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
			cursor:  0, // anthropic
		}
		m.modelSelector = newModelSelectorModel("anthropic", 80)
		m.modelSelector.cursor = 0             // valid model
		m.apiKeyInput.SetValue("sk-valid-key") // non-empty API key

		result, _ := m.advance()
		wm := result.(WizardModel)

		// Should have no validation error
		if wm.validationErr != "" {
			t.Errorf("validationErr should be empty, got %q", wm.validationErr)
		}

		// Should have no warning
		if wm.warningMsg != "" {
			t.Errorf("warningMsg should be empty for non-empty API key, got %q", wm.warningMsg)
		}

		// Should have advanced to next step
		if wm.step != stepChannel {
			t.Errorf("step = %v, want %v", wm.step, stepChannel)
		}
	})
}

func TestParseAllowedUsers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int64
	}{
		{"normal", "123,456,789", []int64{123, 456, 789}},
		{"with spaces", "  123 , 456 , 789 ", []int64{123, 456, 789}},
		{"with bad token", "123456, 789012, bad_token", []int64{123456, 789012}},
		{"empty string", "", nil},
		{"only bad tokens", "abc, def", nil},
		{"single id", "42", []int64{42}},
		{"negative id", "-1", []int64{-1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowedUsers(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAllowedUsers(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func indexOf(s string, slice []string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

func TestBuildConfig_StoreTypeIsFileForAllChannels(t *testing.T) {
	tests := []struct {
		name    string
		channel string
	}{
		{"CLI", "cli"},
		{"Telegram", "telegram"},
		{"Discord", "discord"},
		{"WhatsApp", "whatsapp"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a minimal WizardModel with the channel selected
			m := WizardModel{
				channelSelector: selectorModel{
					cursor:  0,
					choices: []string{"cli", "telegram", "discord", "whatsapp"},
				},
				storePathInput: textinput.New(),
			}
			// Set the channel selection
			m.channelSelector.cursor = indexOf(tc.channel, m.channelSelector.choices)

			cfg := m.buildConfig()

			if cfg.Store.Type != "file" {
				t.Errorf("buildConfig() with channel=%q produced store.type=%q, want \"file\"", tc.channel, cfg.Store.Type)
			}

			// Also check audit.type
			if cfg.Audit.Type != "file" {
				t.Errorf("buildConfig() with channel=%q produced audit.type=%q, want \"file\"", tc.channel, cfg.Audit.Type)
			}
		})
	}
}

func TestBuildConfig_WhatsAppFieldsPresent(t *testing.T) {
	// Create a minimal WizardModel with WhatsApp selected
	tokenInput := textinput.New()
	tokenInput.SetValue("whatsapp-access-token-123")

	allowedUsersInput := textinput.New()
	allowedUsersInput.SetValue("123456789012")

	m := WizardModel{
		channelSelector: selectorModel{
			cursor:  3, // whatsapp is 4th choice (0=cli, 1=telegram, 2=discord, 3=whatsapp)
			choices: []string{"cli", "telegram", "discord", "whatsapp"},
		},
		tokenInput:        tokenInput,
		allowedUsersInput: allowedUsersInput,
		storePathInput:    textinput.New(),
	}

	cfg := m.buildConfig()

	// Check that WhatsApp-specific fields are populated
	if cfg.Channel.AccessToken != "whatsapp-access-token-123" {
		t.Errorf("buildConfig() with channel=whatsapp produced AccessToken=%q, want %q", cfg.Channel.AccessToken, "whatsapp-access-token-123")
	}
	if cfg.Channel.PhoneNumberID != "123456789012" {
		t.Errorf("buildConfig() with channel=whatsapp produced PhoneNumberID=%q, want %q", cfg.Channel.PhoneNumberID, "123456789012")
	}
	if cfg.Channel.VerifyToken != "" {
		t.Errorf("buildConfig() with channel=whatsapp produced VerifyToken=%q, want empty string", cfg.Channel.VerifyToken)
	}
}
