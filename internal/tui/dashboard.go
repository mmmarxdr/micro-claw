package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"microagent/internal/config"
)

// dashTab identifies which tab is active in the dashboard.
type dashTab int

const (
	tabOverview    dashTab = iota // 0
	tabAuditEvents                // 1
	tabStore                      // 2
	tabConfig                     // 3
	tabCount       = 4
)

var tabNames = [tabCount]string{"Overview", "Audit Events", "Store", "Config"}

// dataLoadedMsg carries the result of the one-shot data load from Init.
type dataLoadedMsg struct {
	overview    OverviewData
	auditEvents []AuditEventRow
	storeStats  StoreStats
	err         error
}

// dashStyles holds all lipgloss styles used by the dashboard.
type dashStyles struct {
	activeTab   lipgloss.Style
	inactiveTab lipgloss.Style
	hint        lipgloss.Style
	label       lipgloss.Style
	dimLabel    lipgloss.Style
	border      lipgloss.Style
}

func newDashStyles() dashStyles {
	return dashStyles{
		activeTab:   lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("205")),
		inactiveTab: lipgloss.NewStyle().Faint(true),
		hint:        lipgloss.NewStyle().Faint(true).Italic(true),
		label:       lipgloss.NewStyle().Bold(true),
		dimLabel:    lipgloss.NewStyle().Faint(true),
		border:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
	}
}

// DashboardModel is the top-level Bubbletea model for the read-only dashboard.
type DashboardModel struct {
	activeTab dashTab
	cfg       *config.Config

	// Data loaded once at Init.
	overview    OverviewData
	auditEvents []AuditEventRow
	storeStats  StoreStats
	loadErr     error

	// bubbles/table for Audit Events tab.
	auditTable table.Model

	// terminal dimensions.
	width  int
	height int

	styles dashStyles
}

// newDashboardModel creates a DashboardModel for the given config.
func newDashboardModel(cfg *config.Config) DashboardModel {
	return DashboardModel{
		cfg:    cfg,
		styles: newDashStyles(),
	}
}

// Init returns a one-shot Cmd that loads all data and returns a dataLoadedMsg.
// This fires exactly once; the handler does NOT re-trigger any data load.
func (m DashboardModel) Init() tea.Cmd {
	return func() tea.Msg {
		overview, auditEvents, storeStats, err := LoadAll(m.cfg)
		return dataLoadedMsg{
			overview:    overview,
			auditEvents: auditEvents,
			storeStats:  storeStats,
			err:         err,
		}
	}
}

// Update handles messages and returns the updated model plus an optional Cmd.
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right":
			m.activeTab = (m.activeTab + 1) % tabCount
			return m, nil
		case "left":
			m.activeTab = (m.activeTab + tabCount - 1) % tabCount
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.auditTable.SetWidth(msg.Width - 4)
		return m, nil

	case dataLoadedMsg:
		// Populate data fields — do NOT re-trigger any data load.
		m.overview = msg.overview
		m.auditEvents = msg.auditEvents
		m.storeStats = msg.storeStats
		m.loadErr = msg.err
		m.auditTable = buildAuditTable(msg.auditEvents)
		return m, nil
	}

	// Delegate remaining messages to auditTable for scrolling.
	var cmd tea.Cmd
	m.auditTable, cmd = m.auditTable.Update(msg)
	return m, cmd
}

// View composes the full dashboard view.
func (m DashboardModel) View() string {
	tabs := renderTabBar(m)

	var content string
	switch m.activeTab {
	case tabOverview:
		content = renderOverview(m)
	case tabAuditEvents:
		content = renderAuditEvents(m)
	case tabStore:
		content = renderStore(m)
	case tabConfig:
		content = renderConfig(m)
	default:
		content = ""
	}

	footer := m.styles.hint.Render("Tab/←/→ switch • q quit")
	return lipgloss.JoinVertical(lipgloss.Left, tabs, content, footer)
}

// renderTabBar renders the horizontal tab bar.
func renderTabBar(m DashboardModel) string {
	parts := make([]string, tabCount)
	for i, name := range tabNames {
		if dashTab(i) == m.activeTab {
			parts[i] = m.styles.activeTab.Render("[ " + name + " ]")
		} else {
			parts[i] = m.styles.inactiveTab.Render("  " + name + "  ")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// renderOverview renders the Overview tab content.
func renderOverview(m DashboardModel) string {
	if m.overview.NoData {
		return m.styles.dimLabel.Render("No audit data yet.")
	}
	var sb strings.Builder
	sb.WriteString(m.styles.label.Render("Audit DB:") + " " + m.overview.AuditDBPath + "\n")
	sb.WriteString(m.styles.label.Render("Total Events:") + " " + strconv.FormatInt(m.overview.TotalEvents, 10) + "\n")
	sb.WriteString(m.styles.label.Render("LLM Calls:") + " " + strconv.FormatInt(m.overview.LLMCalls, 10) + "\n")
	sb.WriteString(fmt.Sprintf("%s %.1f in / %.1f out (avg tokens)\n",
		m.styles.label.Render("Avg Tokens:"), m.overview.AvgTokensIn, m.overview.AvgTokensOut))
	sb.WriteString(m.styles.label.Render("Tool Calls:") + " " + strconv.FormatInt(m.overview.ToolCalls, 10) + "\n")
	sb.WriteString(fmt.Sprintf("%s %.1f%%\n",
		m.styles.label.Render("Tool Success Rate:"), m.overview.ToolSuccessRate))
	if m.overview.LastEventAt != "" {
		sb.WriteString(m.styles.label.Render("Last Event:") + " " + m.overview.LastEventAt + "\n")
	}
	if m.loadErr != nil {
		sb.WriteString(m.styles.dimLabel.Render("Load error: "+m.loadErr.Error()) + "\n")
	}
	return sb.String()
}

// renderAuditEvents renders the Audit Events tab content.
func renderAuditEvents(m DashboardModel) string {
	if len(m.auditEvents) == 0 {
		return m.styles.dimLabel.Render("No audit events recorded yet.")
	}
	return m.auditTable.View()
}

// renderStore renders the Store tab content.
func renderStore(m DashboardModel) string {
	if m.storeStats.NoData {
		return m.styles.dimLabel.Render("No store data yet.")
	}
	var sb strings.Builder
	sb.WriteString(m.styles.label.Render("Conversations:") + " " + strconv.FormatInt(m.storeStats.Conversations, 10) + "\n")
	sb.WriteString(m.styles.label.Render("Memory Entries:") + " " + strconv.FormatInt(m.storeStats.MemoryEntries, 10) + "\n")
	sb.WriteString(m.styles.label.Render("Secrets:") + " " + strconv.FormatInt(m.storeStats.Secrets, 10) + "\n")
	return sb.String()
}

// renderConfig renders the Config tab content.
// The API key is always redacted as "***".
func renderConfig(m DashboardModel) string {
	if m.cfg == nil {
		return m.styles.dimLabel.Render("No config loaded.")
	}
	var sb strings.Builder
	sb.WriteString(m.styles.label.Render("Provider:") + " " + m.cfg.Provider.Type + "\n")
	sb.WriteString(m.styles.label.Render("Model:") + " " + m.cfg.Provider.Model + "\n")
	sb.WriteString(m.styles.label.Render("API Key:") + " ***\n")
	sb.WriteString(m.styles.label.Render("Channel Type:") + " " + m.cfg.Channel.Type + "\n")
	sb.WriteString(m.styles.label.Render("Store Path:") + " " + m.cfg.Store.Path + "\n")
	sb.WriteString(m.styles.label.Render("Audit Path:") + " " + m.cfg.Audit.Path + "\n")
	return sb.String()
}

// buildAuditTable constructs a bubbles/table model from audit event rows.
func buildAuditTable(events []AuditEventRow) table.Model {
	columns := []table.Column{
		{Title: "ID", Width: 8},
		{Title: "Type", Width: 12},
		{Title: "Model", Width: 24},
		{Title: "Tokens In", Width: 10},
		{Title: "Tokens Out", Width: 10},
		{Title: "Duration ms", Width: 12},
		{Title: "Tool OK", Width: 8},
	}

	rows := make([]table.Row, 0, len(events))
	for _, e := range events {
		toolOK := "-"
		if e.EventType == "tool_call" {
			if e.ToolOK {
				toolOK = "yes"
			} else {
				toolOK = "no"
			}
		}
		rows = append(rows, table.Row{
			e.ID,
			e.EventType,
			e.Model,
			strconv.FormatInt(e.TokensIn, 10),
			strconv.FormatInt(e.TokensOut, 10),
			strconv.FormatInt(e.DurationMs, 10),
			toolOK,
		})
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(20),
	)
	return t
}

// RunDashboard opens the read-only tab-based dashboard TUI.
// Assumes stdout is a TTY (caller must check).
func RunDashboard(cfg *config.Config) error {
	m := newDashboardModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("dashboard: program error: %w", err)
	}
	return nil
}
