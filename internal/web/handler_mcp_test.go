package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/mcp"
)

// fakeMCPService implements MCPManager.
type fakeMCPService struct {
	servers  []mcp.ServerStatus
	err      error
	addErr   error
	removeErr error
	testTools []string
	testErr  error
}

func (f *fakeMCPService) List(_ context.Context) ([]mcp.ServerStatus, error) {
	return f.servers, f.err
}

func (f *fakeMCPService) Add(_ context.Context, _ config.MCPServerConfig) error {
	return f.addErr
}

func (f *fakeMCPService) Remove(_ context.Context, _ string) error {
	return f.removeErr
}

func (f *fakeMCPService) Test(_ context.Context, _ config.MCPServerConfig) ([]string, error) {
	return f.testTools, f.testErr
}

func newTestServerWithMCP(t *testing.T, st *noWebStore, svc MCPManager) *Server {
	t.Helper()

	s := &Server{
		deps: ServerDeps{
			Store:      st,
			MCPService: svc,
			StartedAt:  time.Now(),
			Config:     &config.Config{},
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	return s
}

func TestHandleListMCPServers_withServers(t *testing.T) {
	svc := &fakeMCPService{
		servers: []mcp.ServerStatus{
			{
				Config: config.MCPServerConfig{
					Name:      "my-server",
					Transport: "stdio",
					Command:   []string{"npx", "mcp-server"},
				},
				Connected: false,
				ToolCount: 0,
			},
			{
				Config: config.MCPServerConfig{
					Name:      "remote",
					Transport: "http",
					URL:       "http://localhost:9000",
				},
				Connected: true,
				ToolCount: 3,
			},
		},
	}

	srv := newTestServerWithMCP(t, &noWebStore{}, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	servers, ok := resp["servers"].([]any)
	if !ok {
		t.Fatalf("expected servers array, got %T", resp["servers"])
	}

	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}

	first := servers[0].(map[string]any)
	if first["name"] != "my-server" {
		t.Errorf("expected name=my-server, got %v", first["name"])
	}

	if first["transport"] != "stdio" {
		t.Errorf("expected transport=stdio, got %v", first["transport"])
	}

	if first["command"] != "npx mcp-server" {
		t.Errorf("expected command='npx mcp-server', got %v", first["command"])
	}

	second := servers[1].(map[string]any)
	if second["url"] != "http://localhost:9000" {
		t.Errorf("expected url=http://localhost:9000, got %v", second["url"])
	}

	if second["connected"] != true {
		t.Errorf("expected connected=true, got %v", second["connected"])
	}

	if int(second["tool_count"].(float64)) != 3 {
		t.Errorf("expected tool_count=3, got %v", second["tool_count"])
	}
}

func TestHandleListMCPServers_emptyList(t *testing.T) {
	svc := &fakeMCPService{servers: []mcp.ServerStatus{}}
	srv := newTestServerWithMCP(t, &noWebStore{}, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	servers, ok := resp["servers"].([]any)
	if !ok {
		t.Fatalf("expected servers array, got %T", resp["servers"])
	}

	if len(servers) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(servers))
	}
}

func TestHandleListMCPServers_nilMCPService(t *testing.T) {
	srv := newTestServerWithMCP(t, &noWebStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	servers, ok := resp["servers"].([]any)
	if !ok {
		t.Fatalf("expected servers array, got %T", resp["servers"])
	}

	if len(servers) != 0 {
		t.Fatalf("expected 0 servers for nil service, got %d", len(servers))
	}
}
