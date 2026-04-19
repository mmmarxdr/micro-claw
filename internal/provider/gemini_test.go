package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"daimon/internal/config"
	"daimon/internal/content"
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
			{Role: "user", Content: content.TextBlock("Hi!")},
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
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("List my files")}},
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
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("test")}},
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
		Messages:     []ChatMessage{{Role: "user", Content: content.TextBlock("hello")}},
	})

	if received.SystemInstruction == nil {
		t.Fatal("expected systemInstruction to be set")
	}
	if len(received.SystemInstruction.Parts) == 0 || received.SystemInstruction.Parts[0].Text != "Be concise." {
		t.Errorf("expected system instruction text 'Be concise.', got %v", received.SystemInstruction)
	}
}

// ─── Embed tests ─────────────────────────────────────────────────────────────

func TestGeminiProvider_Embed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "embedContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("expected key=test-key, got %q", r.URL.Query().Get("key"))
		}

		// Return a synthetic 256-dim embedding.
		values := make([]float64, 256)
		for i := range values {
			values[i] = float64(i) * 0.001
		}
		resp := map[string]any{
			"embedding": map[string]any{
				"values": values,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	vec, err := p.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 256 {
		t.Errorf("expected 256 dims, got %d", len(vec))
	}
}

func TestGeminiProvider_Embed_Error401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"API key invalid"}}`))
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	_, err := p.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestGeminiProvider_Embed_EmptyValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"embedding": map[string]any{
				"values": []float64{},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newTestGeminiProvider(srv.URL)
	_, err := p.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty values")
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

// ---- TestGeminiMultimodalRequest --------------------------------------------

// TestGeminiMultimodalRequest verifies that a user message containing a
// BlockText + BlockAudio is translated to the correct Gemini API wire shape.
// This specifically covers the audio-block path since Gemini supports audio natively.
// The expected shape is stored in testdata/gemini_multimodal_request.json.
// Comparison is map-based (JSON unmarshal → re-marshal) so key ordering does
// not cause false failures.
func TestGeminiMultimodalRequest(t *testing.T) {
	// OGG magic bytes — a distinct payload covering the audio path.
	oggMagic := []byte{0x4F, 0x67, 0x67, 0x53}

	stub := &stubMediaReader{
		entries: map[string]stubMediaEntry{
			"audio789": {data: oggMagic, mime: "audio/ogg"},
		},
	}

	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := readAll(r.Body)
		if err != nil {
			t.Logf("read body error: %v", err)
		}
		capturedBody = b
		// Return a minimal valid Gemini response.
		resp := geminiOKResponse("ok")
		respBytes, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	}))
	defer ts.Close()

	prov := newTestGeminiProvider(ts.URL).WithMediaReader(stub)

	req := ChatRequest{
		Messages: []ChatMessage{
			{
				Role: "user",
				Content: content.Blocks{
					{Type: content.BlockText, Text: "listen"},
					{Type: content.BlockAudio, MediaSHA256: "audio789", MIME: "audio/ogg", Size: 512},
				},
			},
		},
	}

	_, callErr := prov.Chat(context.Background(), req)
	if callErr != nil {
		t.Fatalf("Chat() error: %v", callErr)
	}

	var actual map[string]any
	if err := json.Unmarshal(capturedBody, &actual); err != nil {
		t.Fatalf("unmarshal actual body: %v", err)
	}

	goldenPath := "testdata/gemini_multimodal_request.json"
	goldenRaw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file %s: %v", goldenPath, err)
	}
	var golden map[string]any
	if err := json.Unmarshal(goldenRaw, &golden); err != nil {
		t.Fatalf("unmarshal golden file: %v", err)
	}

	actualCanon, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("re-marshal actual: %v", err)
	}
	goldenCanon, err := json.Marshal(golden)
	if err != nil {
		t.Fatalf("re-marshal golden: %v", err)
	}

	if string(actualCanon) != string(goldenCanon) {
		t.Errorf("multimodal request body does not match golden file %s\n\ngot:\n%s\n\nwant:\n%s",
			goldenPath, capturedBody, goldenRaw)
	}
}
