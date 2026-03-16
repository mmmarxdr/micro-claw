package setup

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"microagent/internal/config"
)

// wizardStep identifies which step of the setup wizard is active.
type wizardStep int

const (
	stepProvider     wizardStep = iota // 0: select provider type
	stepCredentials                    // 1: model + API key
	stepChannel                        // 2: select channel type
	stepChannelExtra                   // 3: bot token + allowed users (telegram/discord only)
	stepStorePath                      // 4: data store path
	stepConfirm                        // 5: YAML preview; Enter to write
	stepDone                           // 6: success message
)

// nextStep returns the step that follows current given the wizard state.
// Pure function — contains all conditional skip logic.
func nextStep(current wizardStep, channelType string) wizardStep {
	switch current {
	case stepChannel:
		if channelType == "telegram" || channelType == "discord" {
			return stepChannelExtra
		}
		return stepStorePath
	case stepChannelExtra:
		return stepStorePath
	default:
		return current + 1
	}
}

// prevStep mirrors nextStep for back-navigation.
// Pure function — clamps at step 0.
func prevStep(current wizardStep, channelType string) wizardStep {
	switch current {
	case stepStorePath:
		if channelType == "telegram" || channelType == "discord" {
			return stepChannelExtra
		}
		return stepChannel
	case stepChannelExtra:
		return stepChannel
	default:
		if current == 0 {
			return 0
		}
		return current - 1
	}
}

// defaultModelForProvider returns a sensible model default for the given provider.
// Pure function — fully testable without a terminal.
func defaultModelForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-sonnet-4-6"
	case "gemini":
		return "gemini-3.1-flash-lite"
	case "openrouter":
		return "openrouter/free"
	case "openai":
		return "gpt-5.4"
	case "ollama":
		return "llama3.2"
	default:
		return ""
	}
}

// selectorModel is a minimal keyboard-driven single-select component.
// Rendered with "> item" for selected, "  item" for others.
type selectorModel struct {
	choices []string
	cursor  int
}

// Update processes key messages to move the cursor.
func (s *selectorModel) Update(msg tea.KeyMsg) {
	switch msg.Type { //nolint:exhaustive
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
		}
	case tea.KeyDown:
		if s.cursor < len(s.choices)-1 {
			s.cursor++
		}
	}
}

// Selected returns the currently highlighted choice.
func (s selectorModel) Selected() string {
	if len(s.choices) == 0 {
		return ""
	}
	return s.choices[s.cursor]
}

// View renders the selector using the provided styles.
func (s selectorModel) View(active lipgloss.Style, inactive lipgloss.Style) string {
	var sb strings.Builder
	for i, choice := range s.choices {
		if i == s.cursor {
			sb.WriteString(active.Render("> " + choice))
		} else {
			sb.WriteString(inactive.Render("  " + choice))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// modelFreeTextPlaceholder returns a placeholder string for the model free-text input.
func modelFreeTextPlaceholder(provider string) string {
	switch provider {
	case "ollama":
		return "e.g. llama3.2"
	default:
		return "e.g. my-custom-model-id"
	}
}

// formatContextK formats a context window size (in thousands of tokens) for display.
func formatContextK(k int) string {
	switch k {
	case 0:
		return "?"
	case 400:
		return ">400K"
	case 1000:
		return "1M"
	default:
		return fmt.Sprintf("%dK", k)
	}
}

// formatCost formats cost-in and cost-out for display in the model selector.
func formatCost(costIn, costOut float64) string {
	if costIn == 0 && costOut == 0 {
		return "free"
	}
	return fmt.Sprintf("$%.2f/$%.2f/1M", costIn, costOut)
}

// modelSelectorModel is a Bubbletea sub-component for picking a model at stepCredentials.
// In list mode it renders a fixed-column metadata table of provider models.
// In freeTextMode it renders a single textinput for manually entering a model ID.
// Switching to freeTextMode is triggered by selecting OtherModelSentinel or when the
// provider is "ollama" (no catalog; always starts in freeTextMode).
type modelSelectorModel struct {
	// items holds the provider catalog entries + OtherModelSentinel appended.
	// For ollama (nil catalog) items is nil and freeTextMode starts true.
	items        []ModelInfo
	cursor       int
	freeTextMode bool
	textInput    textinput.Model
	width        int // terminal width for column truncation
}

// newModelSelectorModel constructs a modelSelectorModel for the given provider.
// For "ollama" (or any provider with nil/empty catalog) it initializes in freeTextMode.
func newModelSelectorModel(provider string, width int) modelSelectorModel {
	catalog := ModelsForProvider(provider)
	ti := textinput.New()
	ti.Placeholder = modelFreeTextPlaceholder(provider)

	if len(catalog) == 0 {
		// ollama or unknown: free-text only; pre-fill with the default model if one exists
		if def := defaultModelForProvider(provider); def != "" {
			ti.SetValue(def)
		}
		ti.Focus()
		return modelSelectorModel{
			items:        nil,
			freeTextMode: true,
			textInput:    ti,
			width:        width,
		}
	}

	// Append OtherModelSentinel so it appears as the last item.
	// The returned slice is owned by this model; catalog is not modified.
	items := make([]ModelInfo, len(catalog)+1)
	copy(items, catalog)
	items[len(catalog)] = OtherModelSentinel

	return modelSelectorModel{
		items:     items,
		cursor:    0,
		textInput: ti,
		width:     width,
	}
}

// Update processes a key message and returns the updated model and a bool
// indicating whether the caller should call advance().
// In list mode: Up/Down moves cursor; Enter selects (transitions to freeTextMode if OtherModelSentinel).
// In freeTextMode: delegates to textInput; Esc returns to list mode (if items != nil).
func (ms modelSelectorModel) Update(msg tea.KeyMsg) (modelSelectorModel, bool) {
	if ms.freeTextMode {
		switch msg.Type {
		case tea.KeyEsc:
			if len(ms.items) > 0 {
				// Return to list mode — only if there is a list to go back to
				ms.freeTextMode = false
				ms.textInput.Blur()
				return ms, false
			}
			// ollama: Esc in free-text with no list → caller handles retreat
			return ms, false
		default:
			var cmd tea.Cmd
			ms.textInput, cmd = ms.textInput.Update(msg)
			_ = cmd // textinput.Blink is handled by WizardModel
			return ms, false
		}
	}

	// List mode
	switch msg.Type { //nolint:exhaustive
	case tea.KeyUp:
		if ms.cursor > 0 {
			ms.cursor--
		}
	case tea.KeyDown:
		if ms.cursor < len(ms.items)-1 {
			ms.cursor++
		}
	case tea.KeyEnter:
		if len(ms.items) > 0 && ms.items[ms.cursor].ID == OtherModelSentinel.ID {
			// Switch to free-text mode; do not advance
			ms.freeTextMode = true
			ms.textInput.Focus()
			return ms, false
		}
		// A real model is selected — signal advance
		return ms, true
	}
	return ms, false
}

// SelectedModelID returns the model ID string to store in config.
// In list mode: returns items[cursor].ID.
// In freeTextMode: returns trimmed textInput.Value().
func (ms modelSelectorModel) SelectedModelID() string {
	if ms.freeTextMode {
		return strings.TrimSpace(ms.textInput.Value())
	}
	if len(ms.items) == 0 || ms.cursor >= len(ms.items) {
		return ""
	}
	return ms.items[ms.cursor].ID
}

// IsReadyToAdvance reports whether the component has a non-empty, valid selection.
// In freeTextMode: returns textInput.Value() != "".
// In list mode: returns true when cursor is not on OtherModelSentinel.
func (ms modelSelectorModel) IsReadyToAdvance() bool {
	if ms.freeTextMode {
		return strings.TrimSpace(ms.textInput.Value()) != ""
	}
	if len(ms.items) == 0 {
		return false
	}
	return ms.items[ms.cursor].ID != OtherModelSentinel.ID
}

// View renders the component.
// In list mode: renders a fixed-column table with adaptive truncation.
//   - width >= 80: show cursor + ID + cost + context + description
//   - width in [60,79]: show cursor + ID + cost + context (no description)
//   - width < 60: show cursor + ID only
//
// In freeTextMode: renders the textInput with a hint line.
// active dims the entire component when focus is elsewhere (e.g. on apiKeyInput).
func (ms modelSelectorModel) View(active bool) string {
	if ms.freeTextMode {
		var sb strings.Builder
		sb.WriteString(ms.textInput.View())
		sb.WriteString("\n")
		if len(ms.items) > 0 {
			// Hint only when there is a list to go back to (not for ollama)
			sb.WriteString("  Esc to return to model list\n")
		}
		return sb.String()
	}

	// List mode: fixed-column table
	var sb strings.Builder
	for i, item := range ms.items {
		cursor := "  "
		if i == ms.cursor {
			cursor = "> "
		}

		var row string
		if item.ID == OtherModelSentinel.ID {
			// Render the sentinel entry simply
			row = cursor + item.DisplayName
		} else if ms.width < 60 {
			// Cursor + ID only
			row = cursor + item.ID
		} else if ms.width < 80 {
			// Cursor + ID + cost + context (no description)
			row = fmt.Sprintf("%s%-28s  %-16s  %s",
				cursor,
				item.ID,
				formatCost(item.CostIn, item.CostOut),
				formatContextK(item.ContextK),
			)
		} else {
			// Full: cursor + ID + cost + context + description
			row = fmt.Sprintf("%s%-28s  %-16s  %-6s  %s",
				cursor,
				item.ID,
				formatCost(item.CostIn, item.CostOut),
				formatContextK(item.ContextK),
				item.Description,
			)
		}

		if i == ms.cursor {
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render(row))
		} else {
			sb.WriteString(lipgloss.NewStyle().Faint(true).Render(row))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// wizardStyles holds all lipgloss styles used by the wizard.
type wizardStyles struct {
	border    lipgloss.Style
	label     lipgloss.Style
	dimLabel  lipgloss.Style
	checkmark lipgloss.Style
	title     lipgloss.Style
	hint      lipgloss.Style
	selected  lipgloss.Style
	inactive  lipgloss.Style
	errStyle  lipgloss.Style
}

func newWizardStyles() wizardStyles {
	return wizardStyles{
		border:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2),
		label:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		dimLabel:  lipgloss.NewStyle().Faint(true),
		checkmark: lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true),
		title:     lipgloss.NewStyle().Bold(true).Underline(true),
		hint:      lipgloss.NewStyle().Faint(true).Italic(true),
		selected:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		inactive:  lipgloss.NewStyle().Faint(true),
		errStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true),
	}
}

// WizardModel is the top-level Bubbletea model for the setup wizard.
// Each step owns its own sub-component(s); Update() dispatches to the
// active step's component(s) and advances/retreats on Enter/Esc.
type WizardModel struct {
	step wizardStep

	// step 0: provider selector
	providerSelector selectorModel

	// step 1: credentials
	modelSelector       modelSelectorModel
	apiKeyInput         textinput.Model
	focusSelectorActive bool // true = modelSelector has focus, false = apiKeyInput has focus

	// step 2: channel selector
	channelSelector selectorModel

	// step 3: channel extras (telegram / discord)
	tokenInput        textinput.Model
	allowedUsersInput textinput.Model
	extFocusIndex     int // 0 = tokenInput, 1 = allowedUsersInput

	// step 4: store path
	storePathInput textinput.Model

	// step 5: confirm
	yamlPreview string

	// step 6: done
	configPath string
	writeErr   error

	// validation feedback — set when advance() is blocked by missing input
	validationErr string

	// terminal dimensions
	width  int
	height int

	// lipgloss styles (initialized once in newWizardModel)
	styles wizardStyles
}

func newWizardModel() WizardModel {
	apiKeyInput := textinput.New()
	apiKeyInput.Placeholder = "sk-..."
	apiKeyInput.EchoMode = textinput.EchoPassword
	apiKeyInput.EchoCharacter = '•'

	tokenInput := textinput.New()
	tokenInput.Placeholder = "Bot token"

	allowedUsersInput := textinput.New()
	allowedUsersInput.Placeholder = "123456,789012 (comma-separated user IDs)"

	storePathInput := textinput.New()
	storePathInput.SetValue("~/.microagent/data")

	return WizardModel{
		step: stepProvider,
		providerSelector: selectorModel{
			choices: []string{"anthropic", "gemini", "openrouter", "openai", "ollama"},
		},
		modelSelector:       newModelSelectorModel("anthropic", 0),
		focusSelectorActive: true,
		apiKeyInput:         apiKeyInput,
		channelSelector: selectorModel{
			choices: []string{"cli", "telegram", "discord"},
		},
		tokenInput:        tokenInput,
		allowedUsersInput: allowedUsersInput,
		storePathInput:    storePathInput,
		styles:            newWizardStyles(),
	}
}

// Init starts the cursor blink for the first active input.
func (m WizardModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update dispatches messages to the active step's component(s).
func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	}
	return m.updateActiveInput(msg)
}

func (m WizardModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type { //nolint:exhaustive
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEnter:
		// At stepCredentials in list mode with selector focused, delegate Enter to modelSelector.
		// The selector signals whether to advance via the returned bool.
		if m.step == stepCredentials && !m.modelSelector.freeTextMode && m.focusSelectorActive {
			updated, shouldAdvance := m.modelSelector.Update(msg)
			m.modelSelector = updated
			if shouldAdvance {
				provider := m.providerSelector.Selected()
				if provider == "ollama" {
					// Ollama has no API key — advance directly
					return m.advance()
				}
				// Model confirmed from list — move focus to API key field
				m.focusSelectorActive = false
				m = m.focusCurrentStep()
				return m, textinput.Blink
			}
			return m, textinput.Blink
		}
		return m.advance()
	case tea.KeyEsc:
		return m.retreat()
	case tea.KeyTab:
		return m.cycleFocus(true)
	case tea.KeyShiftTab:
		return m.cycleFocus(false)
	case tea.KeyUp, tea.KeyDown:
		// Delegate Up/Down to modelSelector when at stepCredentials in list mode with selector focused
		if m.step == stepCredentials && !m.modelSelector.freeTextMode && m.focusSelectorActive {
			updated, _ := m.modelSelector.Update(msg)
			m.modelSelector = updated
			return m, nil
		}
	}
	return m.updateActiveInput(msg)
}

func (m WizardModel) updateActiveInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case stepProvider:
		if key, ok := msg.(tea.KeyMsg); ok {
			m.providerSelector.Update(key)
		}
	case stepCredentials:
		if m.modelSelector.freeTextMode && m.focusSelectorActive {
			// Model free-text field has focus — delegate character input to modelSelector's textInput
			if key, ok := msg.(tea.KeyMsg); ok {
				updated, _ := m.modelSelector.Update(key)
				m.modelSelector = updated
			}
		} else if !m.focusSelectorActive {
			// API key has focus — route ALL input here, whether in list mode or free-text mode
			m.apiKeyInput, cmd = m.apiKeyInput.Update(msg)
		}
		// In list mode with selector focused: Up/Down handled in handleKey; other keys ignored
	case stepChannel:
		if key, ok := msg.(tea.KeyMsg); ok {
			m.channelSelector.Update(key)
		}
	case stepChannelExtra:
		if m.extFocusIndex == 0 {
			m.tokenInput, cmd = m.tokenInput.Update(msg)
		} else {
			m.allowedUsersInput, cmd = m.allowedUsersInput.Update(msg)
		}
	case stepStorePath:
		m.storePathInput, cmd = m.storePathInput.Update(msg)
	}
	return m, cmd
}

func (m WizardModel) advance() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepCredentials:
		provider := m.providerSelector.Selected()
		if provider == "ollama" {
			// Ollama: only require a non-empty model ID; skip api_key check
			if !m.modelSelector.IsReadyToAdvance() {
				m.validationErr = "Please select or enter a model"
				return m, nil
			}
		} else {
			if !m.modelSelector.IsReadyToAdvance() {
				m.validationErr = "Please select or enter a model"
				return m, nil
			}
			if strings.TrimSpace(m.apiKeyInput.Value()) == "" {
				m.validationErr = "API key is required"
				return m, nil
			}
		}
	case stepChannelExtra:
		if strings.TrimSpace(m.tokenInput.Value()) == "" {
			m.validationErr = "Bot token is required"
			return m, nil
		}
	case stepStorePath:
		if strings.TrimSpace(m.storePathInput.Value()) == "" {
			m.validationErr = "Store path is required"
			return m, nil
		}
	case stepConfirm:
		// Write the config file with real (unredacted) values
		path, err := DefaultConfigPath()
		if err != nil {
			m.writeErr = err
			m.step = stepDone
			return m, nil
		}
		cfg := m.buildConfig()
		if werr := WriteConfig(path, cfg); werr != nil {
			m.writeErr = werr
			m.step = stepDone
			return m, nil
		}
		m.configPath = path
		m.step = stepDone
		return m, nil
	case stepDone:
		return m, tea.Quit
	}

	// Clear any validation error on successful advance
	m.validationErr = ""

	// Initialize model selector when advancing from provider step
	if m.step == stepProvider {
		provider := m.providerSelector.Selected()
		m.modelSelector = newModelSelectorModel(provider, m.width)
		m.focusSelectorActive = true
		m.apiKeyInput.Blur()
	}

	// Advance the step
	nextS := nextStep(m.step, m.channelSelector.Selected())
	m.step = nextS

	// Build yaml preview (with redacted secrets) when arriving at confirm step
	if m.step == stepConfirm {
		shadowCfg := m.buildConfig()
		if shadowCfg.Provider.APIKey != "" {
			shadowCfg.Provider.APIKey = "***"
		}
		if shadowCfg.Channel.Token != "" {
			shadowCfg.Channel.Token = "***"
		}
		data, err := marshalAnnotated(shadowCfg)
		if err != nil {
			m.yamlPreview = fmt.Sprintf("(error generating preview: %v)", err)
		} else {
			m.yamlPreview = string(data)
		}
	}

	// Focus the appropriate input for the new step
	m = m.focusCurrentStep()

	return m, textinput.Blink
}

func (m WizardModel) retreat() (tea.Model, tea.Cmd) {
	if m.step == stepProvider {
		// At step 0, Esc = abort
		return m, tea.Quit
	}
	if m.step == stepCredentials {
		// If API key has focus (after selecting a model from list), Esc returns focus
		// to the model list rather than retreating the step.
		if !m.focusSelectorActive && !m.modelSelector.freeTextMode {
			m.focusSelectorActive = true
			m = m.focusCurrentStep()
			return m, textinput.Blink
		}
		// If model selector is in free-text mode and has a list to go back to,
		// Esc returns to list mode rather than retreating the step.
		// (ollama has no list, so Esc goes back to stepProvider)
		if m.modelSelector.freeTextMode && len(m.modelSelector.items) > 0 {
			updated, _ := m.modelSelector.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m.modelSelector = updated
			return m, textinput.Blink
		}
	}
	m.step = prevStep(m.step, m.channelSelector.Selected())
	m.validationErr = ""
	m = m.focusCurrentStep()
	return m, textinput.Blink
}

func (m WizardModel) cycleFocus(_ bool) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepCredentials:
		provider := m.providerSelector.Selected()
		// Tab only cycles between modelSelector.textInput and apiKeyInput
		// when in free-text mode and provider is not ollama.
		// In list mode, Tab is a no-op (arrows navigate the list).
		// For ollama, there is only one field, so Tab is always a no-op.
		if m.modelSelector.freeTextMode && provider != "ollama" {
			m.focusSelectorActive = !m.focusSelectorActive
			m = m.focusCurrentStep()
		}
	case stepChannelExtra:
		m.extFocusIndex = (m.extFocusIndex + 1) % 2
		m = m.focusCurrentStep()
	}
	return m, textinput.Blink
}

func (m WizardModel) focusCurrentStep() WizardModel {
	// Blur all inputs
	m.modelSelector.textInput.Blur()
	m.apiKeyInput.Blur()
	m.tokenInput.Blur()
	m.allowedUsersInput.Blur()
	m.storePathInput.Blur()

	switch m.step {
	case stepCredentials:
		// When focusSelectorActive and in freeTextMode, focus modelSelector's textInput.
		// When focusSelectorActive and in list mode, no textInput to focus — navigation is arrow-key driven.
		// When focusSelectorActive==false, focus apiKeyInput — whether arriving from
		// list mode (Enter on catalog model) or free-text mode (Tab).
		if m.focusSelectorActive {
			if m.modelSelector.freeTextMode {
				m.modelSelector.textInput.Focus()
			}
			// List mode: no textInput to focus — navigation is arrow-key driven
		} else {
			m.modelSelector.textInput.Blur()
			m.apiKeyInput.Focus()
		}
	case stepChannelExtra:
		if m.extFocusIndex == 0 {
			m.tokenInput.Focus()
		} else {
			m.allowedUsersInput.Focus()
		}
	case stepStorePath:
		m.storePathInput.Focus()
	}
	return m
}

func (m WizardModel) buildConfig() *config.Config {
	provider := m.providerSelector.Selected()
	channel := m.channelSelector.Selected()

	cfg := &config.Config{}
	cfg.Provider.Type = provider
	cfg.Provider.Model = m.modelSelector.SelectedModelID()
	cfg.Provider.APIKey = m.apiKeyInput.Value()
	cfg.Channel.Type = channel
	if channel == "telegram" || channel == "discord" {
		cfg.Channel.Token = m.tokenInput.Value()
		cfg.Channel.AllowedUsers = parseAllowedUsers(m.allowedUsersInput.Value())
	}
	cfg.Store.Type = "sqlite"
	cfg.Store.Path = m.storePathInput.Value()
	if cfg.Store.Path == "" {
		cfg.Store.Path = "~/.microagent/data"
	}
	cfg.Audit.Enabled = true
	cfg.Audit.Type = "sqlite"
	cfg.Audit.Path = "~/.microagent/audit"
	return cfg
}

// parseAllowedUsers converts a comma-separated string of integer IDs into
// []int64. Tokens that fail to parse are silently skipped (lenient).
func parseAllowedUsers(raw string) []int64 {
	var ids []int64
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		id, err := strconv.ParseInt(token, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// View renders the current step.
func (m WizardModel) View() string {
	switch m.step {
	case stepProvider:
		return m.viewProvider()
	case stepCredentials:
		return m.viewCredentials()
	case stepChannel:
		return m.viewChannel()
	case stepChannelExtra:
		return m.viewChannelExtra()
	case stepStorePath:
		return m.viewStorePath()
	case stepConfirm:
		return m.viewConfirm()
	case stepDone:
		return m.viewDone()
	default:
		return ""
	}
}

func (m WizardModel) hint() string {
	return m.styles.hint.Render("Enter to continue • Esc to go back • Ctrl+C to quit")
}

func (m WizardModel) viewProvider() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Step 1/5: Select Provider"))
	sb.WriteString("\n\n")
	sb.WriteString(m.providerSelector.View(m.styles.selected, m.styles.inactive))
	sb.WriteString("\n")
	sb.WriteString(m.hint())
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewCredentials() string {
	provider := m.providerSelector.Selected()
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Step 2/5: Model & API Key"))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.label.Render("Model:"))
	sb.WriteString("\n")

	if m.modelSelector.freeTextMode {
		// Free-text mode: show the textInput directly
		sb.WriteString(m.modelSelector.textInput.View())
		sb.WriteString("\n")
		if len(m.modelSelector.items) > 0 {
			// Show "return to list" hint only when there is a list (not for ollama)
			sb.WriteString(m.styles.hint.Render("  Esc to return to model list"))
			sb.WriteString("\n")
		}
	} else {
		// List mode: show model picker
		sb.WriteString(m.modelSelector.View(m.focusSelectorActive))
		sb.WriteString(m.styles.hint.Render("List current as of early 2026 — use Other... for newer models"))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	if provider == "ollama" {
		sb.WriteString(m.styles.hint.Render("API key not required for Ollama"))
	} else {
		sb.WriteString(m.styles.label.Render("API Key:"))
		sb.WriteString("\n")
		sb.WriteString(m.apiKeyInput.View())
	}

	sb.WriteString("\n\n")
	if !m.modelSelector.freeTextMode {
		// List mode: show context-aware hint based on which field has focus
		if m.focusSelectorActive {
			sb.WriteString(m.styles.hint.Render("↑↓ navigate · Enter select model · Ctrl+C quit"))
		} else {
			sb.WriteString(m.styles.hint.Render("Enter to continue · Esc back to model list · Ctrl+C quit"))
		}
	} else {
		if provider != "ollama" {
			sb.WriteString(m.styles.dimLabel.Render("Tab to switch fields"))
			sb.WriteString("\n")
		}
		sb.WriteString(m.hint())
	}
	if m.validationErr != "" {
		sb.WriteString("\n" + m.styles.errStyle.Render("⚠ "+m.validationErr))
	}
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewChannel() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Step 3/5: Select Channel"))
	sb.WriteString("\n\n")
	sb.WriteString(m.channelSelector.View(m.styles.selected, m.styles.inactive))
	sb.WriteString("\n")
	sb.WriteString(m.hint())
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewChannelExtra() string {
	channel := m.channelSelector.Selected()
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render(fmt.Sprintf("Step 3b/5: %s Configuration", strings.ToUpper(channel[:1])+channel[1:])))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.label.Render("Bot Token:"))
	sb.WriteString("\n")
	sb.WriteString(m.tokenInput.View())
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.label.Render("Allowed User IDs (comma-separated):"))
	sb.WriteString("\n")
	sb.WriteString(m.allowedUsersInput.View())
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.dimLabel.Render("Tab to switch fields"))
	sb.WriteString("\n")
	sb.WriteString(m.hint())
	if m.validationErr != "" {
		sb.WriteString("\n" + m.styles.errStyle.Render("⚠ "+m.validationErr))
	}
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewStorePath() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Step 4/5: Data Store Path"))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.label.Render("Store Path:"))
	sb.WriteString("\n")
	sb.WriteString(m.storePathInput.View())
	sb.WriteString("\n\n")
	sb.WriteString(m.hint())
	if m.validationErr != "" {
		sb.WriteString("\n" + m.styles.errStyle.Render("⚠ "+m.validationErr))
	}
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewConfirm() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Step 5/5: Confirm Configuration"))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.dimLabel.Render("The following will be written to ~/.microagent/config.yaml:"))
	sb.WriteString("\n\n")
	sb.WriteString(m.yamlPreview)
	sb.WriteString("\n")
	sb.WriteString(m.hint())
	return m.styles.border.Render(sb.String())
}

func (m WizardModel) viewDone() string {
	var sb strings.Builder
	if m.writeErr != nil {
		sb.WriteString(m.styles.label.Render("❌ Error writing config:"))
		sb.WriteString("\n")
		sb.WriteString(m.writeErr.Error())
	} else {
		sb.WriteString(m.styles.checkmark.Render("✓ Configuration saved!"))
		sb.WriteString("\n\n")
		sb.WriteString(m.styles.dimLabel.Render("Config written to: " + m.configPath))
		sb.WriteString("\n\n")
		provider := m.providerSelector.Selected()
		envVarMap := map[string]string{
			"anthropic":  "ANTHROPIC_API_KEY",
			"gemini":     "GEMINI_API_KEY",
			"openai":     "OPENAI_API_KEY",
			"openrouter": "OPENROUTER_API_KEY",
			"ollama":     "",
		}
		envVar := envVarMap[provider]
		var tip string
		if envVar != "" {
			tip = fmt.Sprintf("Tip: set %s env var and reference it as ${%s} in config for better security.", envVar, envVar)
		} else {
			tip = "Tip: reference env vars as ${VAR_NAME} in config for better security."
		}
		sb.WriteString(m.styles.hint.Render(tip))
	}
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.hint.Render("Press Enter to continue..."))
	return m.styles.border.Render(sb.String())
}

// RunWizard starts the interactive TUI wizard. It assumes stdin is a TTY
// (the caller must check before invoking). Returns the path of the written
// config file, or an error if the user aborted or writing failed.
func RunWizard() (string, error) {
	m := newWizardModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("wizard: program error: %w", err)
	}
	wm, ok := finalModel.(WizardModel)
	if !ok || wm.step != stepDone {
		return "", fmt.Errorf("wizard: aborted by user")
	}
	if wm.writeErr != nil {
		return "", fmt.Errorf("wizard: write config: %w", wm.writeErr)
	}
	return wm.configPath, nil
}
