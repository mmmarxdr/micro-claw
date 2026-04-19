package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"gopkg.in/yaml.v3"

	"daimon/internal/config"
	"daimon/internal/mcp"
)

// ---------------------------------------------------------------------------
// State constants
// ---------------------------------------------------------------------------

type manageState int

const (
	stateList    manageState = iota // 0: table of servers; a=add, d=delete, t=test, q=quit
	stateAdd                        // 1: multi-field add form
	stateConfirm                    // 2: YAML preview; Enter=save, Esc=back to add
	stateTest                       // 3: spinner while Test() runs; shows result
	stateDelete                     // 4: "Remove 'name'? [y/N]" confirmation prompt
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

// manageStyles holds all lipgloss styles for the management screen.
// Kept separate from dashboard styles to keep the two programs independent.
type manageStyles struct {
	title    lipgloss.Style
	label    lipgloss.Style
	dimLabel lipgloss.Style
	hint     lipgloss.Style
	selected lipgloss.Style
	inactive lipgloss.Style
	errStyle lipgloss.Style
	border   lipgloss.Style
	cursor   lipgloss.Style
}

func newManageStyles() manageStyles {
	return manageStyles{
		title:    lipgloss.NewStyle().Bold(true).Underline(true),
		label:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		dimLabel: lipgloss.NewStyle().Faint(true),
		hint:     lipgloss.NewStyle().Faint(true).Italic(true),
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		inactive: lipgloss.NewStyle().Faint(true),
		errStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true),
		border:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2),
		cursor:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	}
}

// ---------------------------------------------------------------------------
// addForm
// ---------------------------------------------------------------------------

// addForm holds all input state for the stateAdd screen.
// focusIndex cycles through: 0=name, 1=transport, 2=command/url, 3=env, 4=prefix
const (
	focusName      = 0
	focusTransport = 1
	focusCmd       = 2 // command (stdio) or url (http)
	focusEnv       = 3
	focusPrefix    = 4
	focusMax       = 4
)

type addForm struct {
	focusIndex   int
	nameInput    textinput.Model
	transport    string // "stdio" or "http"
	commandInput textinput.Model
	urlInput     textinput.Model
	envInput     textinput.Model
	envPairs     []string // accumulated KEY=VALUE pairs
	prefixTools  bool
}

func newAddForm() addForm {
	nameInput := textinput.New()
	nameInput.Placeholder = "e.g. github"
	nameInput.Focus()

	commandInput := textinput.New()
	commandInput.Placeholder = "e.g. npx -y @modelcontextprotocol/server-github"

	urlInput := textinput.New()
	urlInput.Placeholder = "e.g. https://my-mcp.example.com/sse"

	envInput := textinput.New()
	envInput.Placeholder = "KEY=VALUE (Enter to add, empty to skip)"

	return addForm{
		focusIndex:   focusName,
		nameInput:    nameInput,
		transport:    "stdio",
		commandInput: commandInput,
		urlInput:     urlInput,
		envInput:     envInput,
	}
}

// focusActive blurs all inputs then focuses the one matching focusIndex.
func (f *addForm) focusActive() {
	f.nameInput.Blur()
	f.commandInput.Blur()
	f.urlInput.Blur()
	f.envInput.Blur()

	switch f.focusIndex {
	case focusName:
		f.nameInput.Focus()
	case focusCmd:
		if f.transport == "stdio" {
			f.commandInput.Focus()
		} else {
			f.urlInput.Focus()
		}
	case focusEnv:
		f.envInput.Focus()
	}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type serversLoadedMsg struct {
	servers []mcp.ServerStatus
	err     error
}

type testDoneMsg struct {
	tools []string
	err   error
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// MCPManageModel is the top-level Bubbletea model for the MCP management screen.
type MCPManageModel struct {
	state   manageState
	service *mcp.MCPService

	// stateList
	servers []mcp.ServerStatus
	cursor  int
	listMsg string // brief success/error shown after an action

	// stateAdd
	form addForm

	// stateConfirm
	yamlPreview string
	pendingCfg  config.MCPServerConfig

	// stateTest
	testTarget  config.MCPServerConfig
	testSpinner spinner.Model
	testDone    bool
	testTools   []string
	testErr     string

	// stateDelete
	deleteTarget string

	// shared
	validationErr string
	cfgPath       string
	width         int
	height        int
	styles        manageStyles
}

func newMCPManageModel(svc *mcp.MCPService, cfgPath string) MCPManageModel {
	return MCPManageModel{
		state:   stateList,
		service: svc,
		cfgPath: cfgPath,
		styles:  newManageStyles(),
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (m MCPManageModel) Init() tea.Cmd {
	return m.loadServers()
}

func (m MCPManageModel) loadServers() tea.Cmd {
	return func() tea.Msg {
		servers, err := m.service.List(context.Background())
		return serversLoadedMsg{servers: servers, err: err}
	}
}

// ---------------------------------------------------------------------------
// Test command
// ---------------------------------------------------------------------------

func (m MCPManageModel) runTest() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tools, err := m.service.Test(ctx, m.testTarget)
		return testDoneMsg{tools: tools, err: err}
	}
}

func enterTest(m MCPManageModel, target config.MCPServerConfig) (MCPManageModel, tea.Cmd) {
	m.state = stateTest
	m.testTarget = target
	m.testDone = false
	m.testTools = nil
	m.testErr = ""
	m.testSpinner = spinner.New()
	m.testSpinner.Spinner = spinner.Dot
	return m, tea.Batch(m.testSpinner.Tick, m.runTest())
}

// ---------------------------------------------------------------------------
// validateAndConfirm builds the config from the form, validates, and either
// sets validationErr or transitions to stateConfirm.
// ---------------------------------------------------------------------------

func validateAndConfirm(m MCPManageModel) MCPManageModel {
	cfg := config.MCPServerConfig{
		Name:        strings.TrimSpace(m.form.nameInput.Value()),
		Transport:   m.form.transport,
		PrefixTools: m.form.prefixTools,
	}
	if m.form.transport == "stdio" {
		cfg.Command = strings.Fields(strings.TrimSpace(m.form.commandInput.Value()))
	} else {
		cfg.URL = strings.TrimSpace(m.form.urlInput.Value())
	}
	if len(m.form.envPairs) > 0 {
		cfg.Env = make(map[string]string, len(m.form.envPairs))
		for _, pair := range m.form.envPairs {
			k, v, ok := strings.Cut(pair, "=")
			if ok {
				cfg.Env[k] = v
			}
		}
	}

	if err := m.service.Validate(cfg); err != nil {
		m.validationErr = err.Error()
		return m
	}

	// Marshal the config to YAML for preview.
	data, err := yaml.Marshal(cfg)
	if err != nil {
		m.validationErr = fmt.Sprintf("preview error: %v", err)
		return m
	}

	m.validationErr = ""
	m.yamlPreview = string(data)
	m.pendingCfg = cfg
	m.state = stateConfirm
	return m
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m MCPManageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case serversLoadedMsg:
		if msg.err != nil {
			m.listMsg = "Error loading servers: " + msg.err.Error()
		} else {
			m.servers = msg.servers
		}
		if m.cursor >= len(m.servers) {
			m.cursor = max(0, len(m.servers)-1)
		}
		return m, nil

	case spinner.TickMsg:
		if m.state == stateTest && !m.testDone {
			var cmd tea.Cmd
			m.testSpinner, cmd = m.testSpinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case testDoneMsg:
		m.testDone = true
		if msg.err != nil {
			m.testErr = msg.err.Error()
		} else {
			m.testTools = msg.tools
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m MCPManageModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateList:
		return m.handleListKey(msg)
	case stateAdd:
		return m.handleAddKey(msg)
	case stateConfirm:
		return m.handleConfirmKey(msg)
	case stateTest:
		return m.handleTestKey(msg)
	case stateDelete:
		return m.handleDeleteKey(msg)
	}
	return m, nil
}

// --- stateList keys ---

func (m MCPManageModel) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}

	case "down", "j":
		if m.cursor < len(m.servers)-1 {
			m.cursor++
		}

	case "a":
		m.form = newAddForm()
		m.validationErr = ""
		m.state = stateAdd

	case "d":
		if len(m.servers) > 0 {
			m.deleteTarget = m.servers[m.cursor].Config.Name
			m.validationErr = ""
			m.state = stateDelete
		}

	case "t":
		if len(m.servers) > 0 {
			var updated MCPManageModel
			var cmd tea.Cmd
			updated, cmd = enterTest(m, m.servers[m.cursor].Config)
			return updated, cmd
		}
	}
	return m, nil
}

// --- stateAdd keys ---

func (m MCPManageModel) handleAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = stateList
		m.validationErr = ""
		return m, nil

	case "tab":
		m = advanceFocus(m, true)
		return m, textinput.Blink

	case "shift+tab":
		m = advanceFocus(m, false)
		return m, textinput.Blink

	case "enter":
		switch m.form.focusIndex {
		case focusEnv:
			// Add the env pair if non-empty, clear input.
			val := strings.TrimSpace(m.form.envInput.Value())
			if val != "" {
				m.form.envPairs = append(m.form.envPairs, val)
				m.form.envInput.SetValue("")
			}
			// Advance focus to prefix.
			m.form.focusIndex = focusPrefix
			m.form.focusActive()
			return m, textinput.Blink

		case focusPrefix:
			// Final field — validate and confirm.
			m = validateAndConfirm(m)
			return m, nil

		default:
			// Move to next field.
			m = advanceFocus(m, true)
			return m, textinput.Blink
		}

	case " ":
		switch m.form.focusIndex {
		case focusTransport:
			if m.form.transport == "stdio" {
				m.form.transport = "http"
			} else {
				m.form.transport = "stdio"
			}
			// Reset cmd/url inputs when transport changes.
			m.form.commandInput.SetValue("")
			m.form.urlInput.SetValue("")
			return m, nil

		case focusPrefix:
			m.form.prefixTools = !m.form.prefixTools
			return m, nil
		}
		// Fall through to route to active textinput.
	}

	// Route character input to the focused textinput.
	return m.routeToInput(msg)
}

func advanceFocus(m MCPManageModel, forward bool) MCPManageModel {
	if forward {
		m.form.focusIndex++
		if m.form.focusIndex > focusMax {
			m.form.focusIndex = focusMax
		}
	} else {
		m.form.focusIndex--
		if m.form.focusIndex < 0 {
			m.form.focusIndex = 0
		}
	}
	m.form.focusActive()
	return m
}

func (m MCPManageModel) routeToInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.form.focusIndex {
	case focusName:
		m.form.nameInput, cmd = m.form.nameInput.Update(msg)
	case focusCmd:
		if m.form.transport == "stdio" {
			m.form.commandInput, cmd = m.form.commandInput.Update(msg)
		} else {
			m.form.urlInput, cmd = m.form.urlInput.Update(msg)
		}
	case focusEnv:
		m.form.envInput, cmd = m.form.envInput.Update(msg)
	}
	return m, cmd
}

// --- stateConfirm keys ---

func (m MCPManageModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = stateAdd
		return m, nil

	case "enter":
		if err := m.service.Add(context.Background(), m.pendingCfg); err != nil {
			m.validationErr = err.Error()
			return m, nil
		}
		// Saved — now run live test.
		updated, cmd := enterTest(m, m.pendingCfg)
		return updated, cmd

	case "s":
		if err := m.service.Add(context.Background(), m.pendingCfg); err != nil {
			m.validationErr = err.Error()
			return m, nil
		}
		m.listMsg = fmt.Sprintf("Server %q added.", m.pendingCfg.Name)
		m.state = stateList
		return m, m.loadServers()
	}
	return m, nil
}

// --- stateTest keys ---

func (m MCPManageModel) handleTestKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.testDone {
		return m, nil
	}
	// Any key when done — return to list.
	if m.testErr != "" {
		m.listMsg = fmt.Sprintf("Test failed for %q: %s", m.testTarget.Name, m.testErr)
	} else {
		m.listMsg = fmt.Sprintf("Test passed for %q: %d tool(s) discovered.", m.testTarget.Name, len(m.testTools))
	}
	m.state = stateList
	return m, m.loadServers()
}

// --- stateDelete keys ---

func (m MCPManageModel) handleDeleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		if err := m.service.Remove(context.Background(), m.deleteTarget); err != nil {
			m.validationErr = err.Error()
			return m, nil
		}
		m.listMsg = fmt.Sprintf("Server %q removed.", m.deleteTarget)
		m.state = stateList
		return m, m.loadServers()

	case "n", "N", "esc", "enter":
		m.state = stateList
		return m, nil
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m MCPManageModel) View() string {
	switch m.state {
	case stateList:
		return m.viewList()
	case stateAdd:
		return m.viewAdd()
	case stateConfirm:
		return m.viewConfirm()
	case stateTest:
		return m.viewTest()
	case stateDelete:
		return m.viewDelete()
	default:
		return ""
	}
}

// --- stateList view ---

func (m MCPManageModel) viewList() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("MCP Server Management"))
	sb.WriteString("\n\n")

	if m.listMsg != "" {
		sb.WriteString(m.styles.label.Render(m.listMsg))
		sb.WriteString("\n\n")
	}

	if len(m.servers) == 0 {
		sb.WriteString(m.styles.dimLabel.Render("No MCP servers configured. Press 'a' to add one."))
		sb.WriteString("\n\n")
	} else {
		// Header
		header := fmt.Sprintf("  %-20s  %-8s  %-42s  %s",
			"NAME", "TRANSPORT", "COMMAND / URL", "PREFIX")
		sb.WriteString(m.styles.dimLabel.Render(header))
		sb.WriteString("\n")

		for i, srv := range m.servers {
			var cmdURL string
			switch srv.Config.Transport {
			case "stdio":
				cmdURL = strings.Join(srv.Config.Command, " ")
			case "http":
				cmdURL = srv.Config.URL
			}
			if len(cmdURL) > 40 {
				cmdURL = cmdURL[:37] + "..."
			}
			prefixStr := "no"
			if srv.Config.PrefixTools {
				prefixStr = "yes"
			}
			row := fmt.Sprintf("  %-20s  %-8s  %-42s  %s",
				srv.Config.Name, srv.Config.Transport, cmdURL, prefixStr)
			if i == m.cursor {
				sb.WriteString(m.styles.cursor.Render("> " + strings.TrimPrefix(row, "  ")))
			} else {
				sb.WriteString(row)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if m.validationErr != "" {
		sb.WriteString(m.styles.errStyle.Render("⚠ "+m.validationErr))
		sb.WriteString("\n\n")
	}

	sb.WriteString(m.styles.hint.Render("a add  d delete  t test  ↑/↓ navigate  q quit"))
	return m.styles.border.Render(sb.String())
}

// --- stateAdd view ---

func (m MCPManageModel) viewAdd() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Add MCP Server"))
	sb.WriteString("\n\n")

	// Name field
	sb.WriteString(m.fieldLabel("Name", m.form.focusIndex == focusName))
	sb.WriteString(m.form.nameInput.View())
	sb.WriteString("\n\n")

	// Transport selector
	sb.WriteString(m.fieldLabel("Transport", m.form.focusIndex == focusTransport))
	sb.WriteString(m.renderTransportSel())
	sb.WriteString("\n\n")

	// Command or URL
	if m.form.transport == "stdio" {
		sb.WriteString(m.fieldLabel("Command", m.form.focusIndex == focusCmd))
		sb.WriteString(m.form.commandInput.View())
	} else {
		sb.WriteString(m.fieldLabel("URL", m.form.focusIndex == focusCmd))
		sb.WriteString(m.form.urlInput.View())
	}
	sb.WriteString("\n\n")

	// Env pairs accumulated so far
	if len(m.form.envPairs) > 0 {
		sb.WriteString(m.styles.dimLabel.Render("Env vars added:"))
		sb.WriteString("\n")
		for _, p := range m.form.envPairs {
			sb.WriteString(m.styles.dimLabel.Render("  " + p))
			sb.WriteString("\n")
		}
	}
	sb.WriteString(m.fieldLabel("Env (KEY=VALUE)", m.form.focusIndex == focusEnv))
	sb.WriteString(m.form.envInput.View())
	sb.WriteString("\n\n")

	// Prefix toggle
	sb.WriteString(m.fieldLabel("Prefix Tools", m.form.focusIndex == focusPrefix))
	checkBox := "[ ]"
	if m.form.prefixTools {
		checkBox = "[x]"
	}
	if m.form.focusIndex == focusPrefix {
		sb.WriteString(m.styles.selected.Render(checkBox + " prefix tool names with server name"))
	} else {
		sb.WriteString(m.styles.dimLabel.Render(checkBox + " prefix tool names with server name"))
	}
	sb.WriteString("\n\n")

	if m.validationErr != "" {
		sb.WriteString(m.styles.errStyle.Render("⚠ "+m.validationErr))
		sb.WriteString("\n\n")
	}

	sb.WriteString(m.styles.hint.Render("Tab next  Shift+Tab prev  Space toggle  Enter confirm/add-env  Esc cancel"))
	return m.styles.border.Render(sb.String())
}

func (m MCPManageModel) fieldLabel(name string, active bool) string {
	label := name + ":"
	var s string
	if active {
		s = m.styles.label.Render(label)
	} else {
		s = m.styles.dimLabel.Render(label)
	}
	return s + "\n"
}

func (m MCPManageModel) renderTransportSel() string {
	if m.form.transport == "stdio" {
		if m.form.focusIndex == focusTransport {
			return m.styles.selected.Render("[stdio]") + " " + m.styles.inactive.Render("http")
		}
		return m.styles.dimLabel.Render("[stdio] http")
	}
	if m.form.focusIndex == focusTransport {
		return m.styles.inactive.Render("stdio") + " " + m.styles.selected.Render("[http]")
	}
	return m.styles.dimLabel.Render("stdio [http]")
}

// --- stateConfirm view ---

func (m MCPManageModel) viewConfirm() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Confirm New Server"))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.dimLabel.Render("This will be added to your config:"))
	sb.WriteString("\n\n")
	sb.WriteString(m.styles.border.Render(m.yamlPreview))
	sb.WriteString("\n\n")

	if m.validationErr != "" {
		sb.WriteString(m.styles.errStyle.Render("⚠ "+m.validationErr))
		sb.WriteString("\n\n")
	}

	sb.WriteString(m.styles.hint.Render("Enter confirm+test  s save-only  Esc back to edit"))
	return m.styles.border.Render(sb.String())
}

// --- stateTest view ---

func (m MCPManageModel) viewTest() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Testing Connection"))
	sb.WriteString("\n\n")

	if !m.testDone {
		sb.WriteString(m.testSpinner.View())
		sb.WriteString(" ")
		sb.WriteString(fmt.Sprintf("Testing connection to %q...", m.testTarget.Name))
		return m.styles.border.Render(sb.String())
	}

	if m.testErr != "" {
		sb.WriteString(m.styles.errStyle.Render("Connection failed:"))
		sb.WriteString("\n")
		sb.WriteString(m.testErr)
	} else {
		sb.WriteString(m.styles.label.Render(
			fmt.Sprintf("Connected to %q. Discovered %d tool(s):", m.testTarget.Name, len(m.testTools))))
		sb.WriteString("\n")
		for _, t := range m.testTools {
			sb.WriteString("  " + t + "\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(m.styles.hint.Render("Press any key to return to list."))
	return m.styles.border.Render(sb.String())
}

// --- stateDelete view ---

func (m MCPManageModel) viewDelete() string {
	var sb strings.Builder
	sb.WriteString(m.styles.title.Render("Remove Server"))
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("Remove server %q?\n", m.deleteTarget))
	sb.WriteString(m.styles.dimLabel.Render("This cannot be undone."))
	sb.WriteString("\n\n")

	if m.validationErr != "" {
		sb.WriteString(m.styles.errStyle.Render("⚠ "+m.validationErr))
		sb.WriteString("\n\n")
	}

	sb.WriteString(m.styles.hint.Render("y confirm  n cancel"))
	return m.styles.border.Render(sb.String())
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// RunMCPManage starts the TUI MCP management screen.
// cfgPath is the resolved config file path (passed from CLI dispatch).
// Requires a TTY — returns an error if stdin is not a terminal.
func RunMCPManage(cfgPath string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("mcp manage requires a terminal (stdin is not a TTY)")
	}

	resolved, err := config.FindConfigPath(cfgPath)
	if err != nil {
		return fmt.Errorf("mcp manage: %w", err)
	}

	svc := mcp.NewMCPService(resolved)
	m := newMCPManageModel(svc, resolved)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("mcp manage: program error: %w", err)
	}
	return nil
}

