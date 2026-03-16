package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"microagent/internal/config"
)

// geminiOKResponse builds a minimal valid Gemini API response.
func geminiOKResponse(text string) geminiResponse {
	return geminiResponse{
		Candidates: []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		}{
			{
				FinishReason: "STOP",
				Content: geminiContent{
					Role:  "model",
					Parts: []geminiPart{{Text: text}},
				},
			},
		},
		UsageMetadata: struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		}{PromptTokenCount: 10, CandidatesTokenCount: 5},
	}
}

func geminiToolCallResponse(toolName string, args map[string]any) geminiResponse {
	return geminiResponse{
		Candidates: []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		}{
			{
				FinishReason: "STOP",
				Content: geminiContent{
					Role: "model",
					Parts: []geminiPart{{
						FunctionCall: &geminiFunctionCall{
							Name: toolName,
							Args: args,
						},
					}},
				},
			},
		},
		UsageMetadata: struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		}{PromptTokenCount: 20, CandidatesTokenCount: 8},
	}
}

func newTestGeminiProvider(serverURL string) *GeminiProvider {
	return NewGeminiProvider(config.ProviderConfig{
		APIKey:  "test-key",
		Model:   "gemini-2.0-flash",
		BaseURL: serverURL,
	})
}

func TestGeminiProvider_Name(t *testing.T) {
	p := newTestGeminiProvider("http://dummy")
	if p.Name() != "gemini" {
		t.Errorf("expected 'gemini', got %q", p.Name())
	}
}

func TestGeminiProvider_SupportsTools(t *testing.T) {
	p := newTestGeminiProvider("http://dummy")
	if !p.SupportsTools() {
		t.Error("expected SupportsTools() = true")
	}
}

func TestGeminiProvider_Chat_TextResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify URL shape
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("expected key=test-key in query, got %q", r.URL.Query().Get("key"))
		}

		resp := geminiOKResponse("Hello from Gemini!")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	resp, err := p.Chat(context.Background(), ChatRequest{
		SystemPrompt: "You are helpful.",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hi!"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Gemini!" {
		t.Errorf("expected content %q, got %q", "Hello from Gemini!", resp.Content)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("expected InputTokens=10, got %d", resp.Usage.InputTokens)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected StopReason 'end_turn', got %q", resp.StopReason)
	}
}

func TestGeminiProvider_Chat_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiToolCallResponse("shell_exec", map[string]any{"command": "ls -la"})
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "List my files"}},
		Tools: []ToolDefinition{
			{Name: "shell_exec", Description: "Run shell command", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "shell_exec" {
		t.Errorf("expected tool name 'shell_exec', got %q", resp.ToolCalls[0].Name)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("expected StopReason 'tool_use', got %q", resp.StopReason)
	}

	// Verify input was serialised correctly
	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Input, &args); err != nil {
		t.Fatalf("tool input is not valid JSON: %v", err)
	}
	if args["command"] != "ls -la" {
		t.Errorf("expected command='ls -la', got %v", args["command"])
	}
}

func TestGeminiProvider_Chat_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"API key not valid"}}`))
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestGeminiProvider_Chat_SystemPromptIncluded(t *testing.T) {
	var received geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		resp := geminiOKResponse("ok")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	_, _ = p.Chat(context.Background(), ChatRequest{
		SystemPrompt: "Be concise.",
		Messages:     []ChatMessage{{Role: "user", Content: "hello"}},
	})

	if received.SystemInstruction == nil {
		t.Fatal("expected systemInstruction to be set")
	}
	if len(received.SystemInstruction.Parts) == 0 || received.SystemInstruction.Parts[0].Text != "Be concise." {
		t.Errorf("expected system instruction text 'Be concise.', got %v", received.SystemInstruction)
	}
}

func TestNormalizeGeminiFinishReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"STOP", "end_turn"},
		{"MAX_TOKENS", "max_tokens"},
		{"SAFETY", "SAFETY"},
	}
	for _, c := range cases {
		got := normalizeGeminiFinishReason(c.in)
		if got != c.want {
			t.Errorf("normalizeGeminiFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
