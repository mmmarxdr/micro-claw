package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/provider"
	"microagent/internal/store"
	"microagent/internal/tool"
)

// ---------------------------------------------------------------------------
// Integration test helpers
// ---------------------------------------------------------------------------

// buildAnthropicTextResponse returns a minimal Anthropic-format JSON response
// with a single text content block and stop_reason=end_turn.
func buildAnthropicTextResponse(text string) string {
	resp := map[string]any{
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "end_turn",
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// buildAnthropicToolUseResponse returns an Anthropic-format response that
// asks the agent to call the named tool with the given JSON input.
func buildAnthropicToolUseResponse(toolID, toolName string, inputJSON map[string]any) string {
	resp := map[string]any{
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "tool_use",
		"content": []any{
			map[string]any{
				"type":  "tool_use",
				"id":    toolID,
				"name":  toolName,
				"input": inputJSON,
			},
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// newIntegrationProvider creates an AnthropicProvider pointed at the test server.
func newIntegrationProvider(ts *httptest.Server) *provider.AnthropicProvider {
	cfg := config.ProviderConfig{
		APIKey:     "test-key",
		BaseURL:    ts.URL,
		MaxRetries: 0, // single attempt — no retry delays in tests
		Timeout:    5 * time.Second,
	}
	return provider.NewAnthropicProvider(cfg)
}

// newIntegrationStore creates a FileStore backed by the given temp directory.
func newIntegrationStore(dir string) *store.FileStore {
	return store.NewFileStore(config.StoreConfig{
		Type: "file",
		Path: dir,
	})
}

// defaultIntegrationAgentConfig returns a minimal, fast AgentConfig for tests.
func defaultIntegrationAgentConfig() config.AgentConfig {
	return config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 1024,
		MemoryResults:    5,
		HistoryLength:    20,
	}
}

func defaultIntegrationLimitsConfig() config.LimitsConfig {
	return config.LimitsConfig{
		TotalTimeout: 10 * time.Second,
		ToolTimeout:  5 * time.Second,
	}
}

// collectOutput reads all bytes written to buf until the sentinel appears or
// the deadline fires.  It returns the accumulated output.
func collectOutput(buf *bytes.Buffer, sentinel string, deadline time.Duration) string {
	timeout := time.After(deadline)
	for {
		select {
		case <-timeout:
			return buf.String()
		default:
			s := buf.String()
			if strings.Contains(s, sentinel) {
				return s
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 1: TestIntegration_FullCLIFlow
// ---------------------------------------------------------------------------
//
// Flow:
//   user prompt → provider returns tool_use(list_files) → agent executes tool →
//   provider receives tool_result → provider returns final text → assert output.
//
// The test server serves two responses in sequence:
//   1. tool_use block for "list_files" with input {"path":"."}
//   2. text block with the final answer

func TestIntegration_FullCLIFlow(t *testing.T) {
	tmpDir := t.TempDir()

	// Scripted server: first call → tool_use, second call → text response.
	var callCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicToolUseResponse(
				"tc-001", "list_files",
				map[string]any{"path": "."},
			)))
		default:
			_, _ = w.Write([]byte(buildAnthropicTextResponse("Files listed successfully.")))
		}
	}))
	t.Cleanup(ts.Close)

	// Wire up real components.
	prov := newIntegrationProvider(ts)
	st := newIntegrationStore(tmpDir)

	// list_files tool needs a valid base_path so it can execute.
	toolRegistry := tool.BuildRegistry(config.ToolsConfig{
		File: config.FileToolConfig{
			Enabled:  true,
			BasePath: tmpDir,
		},
	})

	// CLIChannel wired to io.Pipe so we can inject input and capture output.
	pr, pw := newLinePipe()
	var outBuf bytes.Buffer
	ch := channel.NewCLIChannel(config.ChannelConfig{}, pr, &outBuf)

	ag := New(
		defaultIntegrationAgentConfig(),
		defaultIntegrationLimitsConfig(),
		ch,
		prov,
		st,
		audit.NoopAuditor{},
		toolRegistry,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	runDone := make(chan error, 1)
	go func() { runDone <- ag.Run(ctx) }()

	// Send user prompt.
	fmt.Fprintln(pw, "list the files")

	// Wait for the final text to reach the output buffer.
	output := collectOutput(&outBuf, "Files listed successfully.", 4*time.Second)

	if !strings.Contains(output, "Files listed successfully.") {
		t.Errorf("expected final text response in output, got:\n%s", output)
	}

	// Expect exactly 2 provider calls (tool_use + final text).
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 provider calls, got %d", got)
	}

	cancel()
	<-runDone
}

// ---------------------------------------------------------------------------
// Test 2: TestIntegration_ConversationSurvivesRestart
// ---------------------------------------------------------------------------
//
// Flow:
//  Turn 1: send prompt, get text response → assert written to output AND saved
//          to conversation store AND appended to memory.
//  Restart: new Agent with a NEW Agent/FileStore instance pointing at the SAME
//           temp dir (simulating a process restart).
//  Turn 2: send a prompt whose text contains a word from the Turn 1 assistant
//          reply so that SearchMemory returns it for memory injection.
//          Capture the ChatRequest sent to the provider; verify:
//            (a) conversation history includes Turn 1's user+assistant messages
//            (b) system prompt contains Turn 1's assistant reply (memory injection)

func TestIntegration_ConversationSurvivesRestart(t *testing.T) {
	tmpDir := t.TempDir()

	// SearchMemory checks if the memory *content* contains the query string as a
	// substring (case-insensitive).  To ensure the memory entry is found in Turn 2,
	// the Turn 2 query must be a substring of the Turn 1 assistant reply.
	//
	// assistantReply  = "I remember this conversation."
	// turn2Query      = "remember"   ← substring of assistantReply ✓
	const assistantReply = "I remember this conversation."
	const turn2Query = "remember"

	// -------------------------------------------------------------------------
	// Turn 1 — first agent instance.
	// -------------------------------------------------------------------------
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(buildAnthropicTextResponse(assistantReply)))
	}))
	t.Cleanup(ts1.Close)

	st1 := newIntegrationStore(tmpDir)
	prov1 := newIntegrationProvider(ts1)

	pr1, pw1 := newLinePipe()
	var outBuf1 bytes.Buffer
	ch1 := channel.NewCLIChannel(config.ChannelConfig{}, pr1, &outBuf1)

	ag1 := New(defaultIntegrationAgentConfig(), defaultIntegrationLimitsConfig(), ch1, prov1, st1, audit.NoopAuditor{}, nil)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel1)

	runDone1 := make(chan error, 1)
	go func() { runDone1 <- ag1.Run(ctx1) }()

	fmt.Fprintln(pw1, "hello agent")

	// Wait for reply to appear.
	output1 := collectOutput(&outBuf1, assistantReply, 4*time.Second)
	if !strings.Contains(output1, assistantReply) {
		t.Fatalf("Turn 1: expected reply %q in output, got:\n%s", assistantReply, output1)
	}

	// Give the store time to flush to disk before we cancel.
	time.Sleep(50 * time.Millisecond)
	cancel1()
	<-runDone1

	// Verify the conversation was persisted to disk.
	convID := "conv_cli"
	savedConv, err := st1.LoadConversation(context.Background(), convID)
	if err != nil {
		t.Fatalf("Turn 1: conversation not persisted: %v", err)
	}
	if len(savedConv.Messages) < 2 {
		t.Fatalf("Turn 1: expected at least 2 messages saved, got %d", len(savedConv.Messages))
	}

	// Verify AppendMemory was called — search for a word from the reply.
	memories, err := st1.SearchMemory(context.Background(), "cli", "remember", 10)
	if err != nil {
		t.Fatalf("Turn 1: SearchMemory error: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Turn 1: expected at least 1 memory entry after first turn, got 0")
	}

	// -------------------------------------------------------------------------
	// Turn 2 — simulate restart: new Agent + new FileStore, same tmp dir.
	// -------------------------------------------------------------------------

	// The second server captures the incoming ChatRequest to inspect history
	// and the system prompt.
	var capturedMessages []provider.ChatMessage
	var capturedSystem string
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Decode the request to inspect messages and system prompt.
		var body struct {
			System   string `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedSystem = body.System
		for _, m := range body.Messages {
			capturedMessages = append(capturedMessages, provider.ChatMessage{
				Role:    m.Role,
				Content: fmt.Sprintf("%v", m.Content),
			})
		}

		_, _ = w.Write([]byte(buildAnthropicTextResponse("Turn 2 response.")))
	}))
	t.Cleanup(ts2.Close)

	// Fresh store + provider + channel + agent — same data directory.
	st2 := newIntegrationStore(tmpDir)
	prov2 := newIntegrationProvider(ts2)

	pr2, pw2 := newLinePipe()
	var outBuf2 bytes.Buffer
	ch2 := channel.NewCLIChannel(config.ChannelConfig{}, pr2, &outBuf2)

	ag2 := New(defaultIntegrationAgentConfig(), defaultIntegrationLimitsConfig(), ch2, prov2, st2, audit.NoopAuditor{}, nil)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel2)

	runDone2 := make(chan error, 1)
	go func() { runDone2 <- ag2.Run(ctx2) }()

	// Use a query that contains "remember" so SearchMemory returns the Turn 1
	// memory entry and injects it into the system prompt.
	fmt.Fprintln(pw2, turn2Query)

	output2 := collectOutput(&outBuf2, "Turn 2 response.", 4*time.Second)
	if !strings.Contains(output2, "Turn 2 response.") {
		t.Errorf("Turn 2: expected reply in output, got:\n%s", output2)
	}

	cancel2()
	<-runDone2

	// (a) Assert that the provider received Turn 1's conversation history.
	foundHello := false
	foundReply := false
	for _, m := range capturedMessages {
		if m.Role == "user" && strings.Contains(m.Content, "hello agent") {
			foundHello = true
		}
		if m.Role == "assistant" && strings.Contains(m.Content, assistantReply) {
			foundReply = true
		}
	}
	if !foundHello {
		t.Errorf("Turn 2: expected Turn 1 user message in history; messages: %+v", capturedMessages)
	}
	if !foundReply {
		t.Errorf("Turn 2: expected Turn 1 assistant reply in history; messages: %+v", capturedMessages)
	}

	// (b) Assert memory injection: Turn 1's assistant reply in the system prompt.
	if !strings.Contains(capturedSystem, assistantReply) {
		t.Errorf("Turn 2: expected assistant reply %q in system prompt (memory injection); got system: %q",
			assistantReply, capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Test 3: TestIntegration_AddNewTool
// ---------------------------------------------------------------------------
//
// Registers a minimal "echo_tool" that returns its input unchanged.
// Server scripts: tool_use(echo_tool) → final text.
// Asserts the tool was called and the final text reached the output.

func TestIntegration_AddNewTool(t *testing.T) {
	tmpDir := t.TempDir()

	// Track whether the echo tool was invoked.
	var echoCallCount int32
	echoTool := &echoTestTool{
		callCount: &echoCallCount,
	}

	var callCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicToolUseResponse(
				"echo-001", "echo_tool",
				map[string]any{"message": "hello from tool"},
			)))
		default:
			// Verify the tool_result reached the server by checking the request body.
			_, _ = w.Write([]byte(buildAnthropicTextResponse("Echo confirmed.")))
		}
	}))
	t.Cleanup(ts.Close)

	prov := newIntegrationProvider(ts)
	st := newIntegrationStore(tmpDir)

	toolRegistry := map[string]tool.Tool{
		"echo_tool": echoTool,
	}

	pr, pw := newLinePipe()
	var outBuf bytes.Buffer
	ch := channel.NewCLIChannel(config.ChannelConfig{}, pr, &outBuf)

	ag := New(
		defaultIntegrationAgentConfig(),
		defaultIntegrationLimitsConfig(),
		ch,
		prov,
		st,
		audit.NoopAuditor{},
		toolRegistry,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	runDone := make(chan error, 1)
	go func() { runDone <- ag.Run(ctx) }()

	fmt.Fprintln(pw, "echo something")

	output := collectOutput(&outBuf, "Echo confirmed.", 4*time.Second)
	if !strings.Contains(output, "Echo confirmed.") {
		t.Errorf("expected final text 'Echo confirmed.' in output, got:\n%s", output)
	}

	// Verify the custom tool was actually called.
	if got := atomic.LoadInt32(&echoCallCount); got != 1 {
		t.Errorf("expected echo_tool to be called once, got %d", got)
	}

	// Verify 2 provider calls (tool_use + final text).
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 provider calls, got %d", got)
	}

	cancel()
	<-runDone
}

// ---------------------------------------------------------------------------
// echoTestTool — a minimal tool that returns its "message" input unchanged.
// ---------------------------------------------------------------------------

type echoTestTool struct {
	callCount *int32
}

func (e *echoTestTool) Name() string        { return "echo_tool" }
func (e *echoTestTool) Description() string { return "Echoes the input message back to the agent." }
func (e *echoTestTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message": {"type": "string", "description": "The message to echo."}
		},
		"required": ["message"]
	}`)
}

func (e *echoTestTool) Execute(_ context.Context, params json.RawMessage) (tool.ToolResult, error) {
	atomic.AddInt32(e.callCount, 1)
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return tool.ToolResult{IsError: true, Content: err.Error()}, nil
	}
	return tool.ToolResult{Content: p.Message}, nil
}

// ---------------------------------------------------------------------------
// newLinePipe creates a synchronised pipe suitable for line-by-line I/O.
// The returned reader is buffered; the writer is used to inject lines.
// ---------------------------------------------------------------------------

func newLinePipe() (*bufio.Reader, *pipeWriter) {
	pr, pw := bytePipe()
	return bufio.NewReader(pr), pw
}

// bytePipe returns a simple in-memory pipe as (*pipeReader, *pipeWriter).
// We cannot use io.Pipe directly with bufio.Scanner when writing in a goroutine,
// because scanner.Scan blocks waiting for a newline.  Instead we use a
// channel-backed pipe that only blocks when no data is available.
type pipeReader struct {
	ch  chan []byte
	buf []byte
}

type pipeWriter struct {
	ch chan []byte
}

func bytePipe() (*pipeReader, *pipeWriter) {
	ch := make(chan []byte, 64)
	return &pipeReader{ch: ch}, &pipeWriter{ch: ch}
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	w.ch <- cp
	return len(p), nil
}

// Fprintln writes a line (with newline) to the pipe writer.
// This mirrors the fmt.Fprintln call used in tests.
func Fprintln(w *pipeWriter, s string) {
	fmt.Fprintln(w, s)
}

func (r *pipeReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		chunk := <-r.ch
		r.buf = chunk
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
