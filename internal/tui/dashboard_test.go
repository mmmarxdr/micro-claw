package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"microagent/internal/config"
)

func TestDashboardModel_TabSwitching(t *testing.T) {
	cfg := &config.Config{}
	m := newDashboardModel(cfg, "")

	// Start at tabOverview (0).
	if m.activeTab != tabOverview {
		t.Fatalf("initial activeTab = %d, want %d (tabOverview)", m.activeTab, tabOverview)
	}

	tabs := []dashTab{tabOverview, tabAuditEvents, tabStore, tabConfig, tabMCP, tabOverview}
	for i, want := range tabs[1:] {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(DashboardModel)
		if m.activeTab != want {
			t.Errorf("after Tab press %d: activeTab = %d, want %d", i+1, m.activeTab, want)
		}
	}
}

func TestDashboardModel_LeftArrowTabSwitching(t *testing.T) {
	cfg := &config.Config{}
	m := newDashboardModel(cfg, "")

	// Left arrow from tabOverview (0) should wrap to tabMCP (4).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(DashboardModel)
	if m.activeTab != tabMCP {
		t.Errorf("left arrow from tabOverview: activeTab = %d, want %d (tabMCP)", m.activeTab, tabMCP)
	}
}

func TestDashboardModel_RightArrowTabSwitching(t *testing.T) {
	cfg := &config.Config{}
	m := newDashboardModel(cfg, "")

	// Right arrow from tabOverview (0) goes to tabAuditEvents (1).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(DashboardModel)
	if m.activeTab != tabAuditEvents {
		t.Errorf("right arrow from tabOverview: activeTab = %d, want %d (tabAuditEvents)", m.activeTab, tabAuditEvents)
	}
}

func TestDashboardModel_QKeyQuits(t *testing.T) {
	cfg := &config.Config{}
	m := newDashboardModel(cfg, "")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected a Cmd from q key, got nil")
	}
	// Execute the cmd and verify it's tea.Quit.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("q key cmd returned %T, want tea.QuitMsg", msg)
	}
}

func TestRenderConfig_RedactsAPIKey(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-real-secret-key"},
		},
		Models: config.ModelsConfig{
			Default: config.ModelRef{Provider: "anthropic", Model: "claude-3-5-sonnet"},
		},
		Channel: config.ChannelConfig{Type: "cli"},
		Store:   config.StoreConfig{Path: "/data"},
		Audit:   config.AuditConfig{Path: "/audit"},
	}

	m := newDashboardModel(cfg, "")
	output := renderConfig(m)

	if strings.Contains(output, "sk-real-secret-key") {
		t.Error("renderConfig must NOT include the real API key value")
	}
	if !strings.Contains(output, "***") {
		t.Error("renderConfig should show *** in place of the API key")
	}
	// Other fields should be visible.
	if !strings.Contains(output, "anthropic") {
		t.Error("renderConfig should show provider type")
	}
	if !strings.Contains(output, "claude-3-5-sonnet") {
		t.Error("renderConfig should show model name")
	}
}

func TestDashboardModel_DataLoadedMsg(t *testing.T) {
	cfg := &config.Config{}
	m := newDashboardModel(cfg, "")

	// Simulate the data load completing.
	loaded := dataLoadedMsg{
		overview: OverviewData{
			TotalEvents: 42,
			LLMCalls:    10,
			NoData:      false,
		},
		auditEvents: []AuditEventRow{
			{ID: "1", EventType: "llm_call", Model: "gpt-4", TokensIn: 100},
		},
		storeStats: StoreStats{Conversations: 5, NoData: false},
		err:        nil,
	}

	updated, cmd := m.Update(loaded)
	m = updated.(DashboardModel)

	if cmd != nil {
		// dataLoadedMsg handler must return nil cmd (no re-dispatch).
		t.Errorf("dataLoadedMsg handler returned non-nil cmd: %v (potential infinite loop)", cmd)
	}
	if m.overview.TotalEvents != 42 {
		t.Errorf("overview.TotalEvents = %d, want 42", m.overview.TotalEvents)
	}
	if len(m.auditEvents) != 1 {
		t.Errorf("len(auditEvents) = %d, want 1", len(m.auditEvents))
	}
}

func TestRenderOverview_NoData(t *testing.T) {
	m := newDashboardModel(&config.Config{}, "")
	m.overview = OverviewData{NoData: true}

	output := renderOverview(m)
	if !strings.Contains(output, "No audit data yet") {
		t.Errorf("renderOverview with NoData should show 'No audit data yet', got: %q", output)
	}
}

func TestRenderStore_NoData(t *testing.T) {
	m := newDashboardModel(&config.Config{}, "")
	m.storeStats = StoreStats{NoData: true}

	output := renderStore(m)
	if !strings.Contains(output, "No store data yet") {
		t.Errorf("renderStore with NoData should show 'No store data yet', got: %q", output)
	}
}

func TestRenderAuditEvents_Empty(t *testing.T) {
	m := newDashboardModel(&config.Config{}, "")
	m.auditEvents = nil

	output := renderAuditEvents(m)
	if !strings.Contains(output, "No audit events recorded yet") {
		t.Errorf("renderAuditEvents with no events should show message, got: %q", output)
	}
}
