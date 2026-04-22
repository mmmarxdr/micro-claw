package main

import (
	"os"
	"strings"
	"testing"
)

// TestWireRAG_PropagatesAllConfig is a regression guard against the class of
// bug where a With*Conf setter on *agent.Agent is defined but never invoked in
// wireRAG, silently ignoring user config. This exact bug shipped in PR #2 —
// rag.retrieval.* was dead for ~24h before an audit caught it.
//
// The test is a source scan, intentionally brittle against method renames.
// The wiring function takes real providers and stores that are expensive to
// mock, so runtime exercise is deferred to manual + integration testing. A
// linter rule (orphaned-setter detection) would be the long-term fix — see
// openspec/changes/audit-dual-path/findings.md § Strategy.
func TestWireRAG_PropagatesAllConfig(t *testing.T) {
	src, err := os.ReadFile("rag_wiring.go")
	if err != nil {
		t.Fatalf("read rag_wiring.go: %v", err)
	}
	content := string(src)
	required := []struct {
		call   string
		reason string
	}{
		{"WithRAGStore(", "document store + embed function"},
		{"WithRAGRetrievalConf(", "rag.retrieval precision (neighbor radius, BM25/cosine thresholds)"},
		{"WithRAGMetrics(", "rag.metrics in-memory ring recorder"},
		{"WithRAGHydeConf(", "rag.hyde configuration + hypothesis function"},
	}
	for _, req := range required {
		if !strings.Contains(content, req.call) {
			t.Errorf("wireRAG must call %s to wire %s — otherwise user config is silently ignored",
				req.call, req.reason)
		}
	}
}

// TestStartupPaths_BothCallWireRAG guards the other known dual-path bug:
// runWebCommand (web_cmd.go) was missing wireRAG for a release, breaking
// /api/knowledge in `daimon web` mode. Any new wiring added to main.go must
// be mirrored in web_cmd.go — this test enforces at least the wireRAG call.
func TestStartupPaths_BothCallWireRAG(t *testing.T) {
	for _, path := range []string{"main.go", "web_cmd.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(src), "wireRAG(") {
			t.Errorf("%s must call wireRAG — dual startup paths must stay in sync", path)
		}
	}
}
