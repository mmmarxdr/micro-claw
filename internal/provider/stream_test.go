package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// NewStreamResult tests
// --------------------------------------------------------------------------

func TestNewStreamResult_CreatesValidChannelPair(t *testing.T) {
	sr, events := NewStreamResult(4)
	if sr == nil {
		t.Fatal("NewStreamResult returned nil StreamResult")
	}
	if sr.Events == nil {
		t.Fatal("StreamResult.Events channel is nil")
	}
	if events == nil {
		t.Fatal("write-only events channel is nil")
	}
}

func TestStreamResult_TextDeltaEvents(t *testing.T) {
	sr, events := NewStreamResult(8)

	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "Hello"}
		events <- StreamEvent{Type: StreamEventTextDelta, Text: " world"}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{Content: "Hello world"}, nil)
	}()

	var chunks []string
	for ev := range sr.Events {
		if ev.Type == StreamEventTextDelta {
			chunks = append(chunks, ev.Text)
		}
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 text delta events, got %d", len(chunks))
	}
	if chunks[0] != "Hello" || chunks[1] != " world" {
		t.Errorf("unexpected chunks: %v", chunks)
	}
}

func TestStreamResult_ResponseAssemblesText(t *testing.T) {
	sr, events := NewStreamResult(8)

	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "Hello"}
		events <- StreamEvent{Type: StreamEventTextDelta, Text: " world"}
		events <- StreamEvent{Type: StreamEventUsage, Usage: &UsageStats{InputTokens: 10, OutputTokens: 5}, StopReason: "end_turn"}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{
			Content:    "Hello world",
			Usage:      UsageStats{InputTokens: 10, OutputTokens: 5},
			StopReason: "end_turn",
		}, nil)
	}()

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Hello world" {
		t.Errorf("expected Content %q, got %q", "Hello world", resp.Content)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("unexpected Usage: %+v", resp.Usage)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected StopReason %q, got %q", "end_turn", resp.StopReason)
	}
}

func TestStreamResult_ResponseAssemblesToolCalls(t *testing.T) {
	sr, events := NewStreamResult(16)

	expectedInput := `{"query":"test"}`
	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "Let me check"}
		events <- StreamEvent{
			Type:       StreamEventToolCallStart,
			ToolCallID: "call_1",
			ToolName:   "search",
		}
		events <- StreamEvent{
			Type:      StreamEventToolCallDelta,
			ToolInput: `{"query":`,
		}
		events <- StreamEvent{
			Type:      StreamEventToolCallDelta,
			ToolInput: `"test"}`,
		}
		events <- StreamEvent{Type: StreamEventToolCallEnd}
		events <- StreamEvent{Type: StreamEventUsage, Usage: &UsageStats{InputTokens: 20, OutputTokens: 15}, StopReason: "tool_use"}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{
			Content: "Let me check",
			ToolCalls: []ToolCall{{
				ID:    "call_1",
				Name:  "search",
				Input: json.RawMessage(expectedInput),
			}},
			Usage:      UsageStats{InputTokens: 20, OutputTokens: 15},
			StopReason: "tool_use",
		}, nil)
	}()

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Let me check" {
		t.Errorf("expected Content %q, got %q", "Let me check", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "search" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if string(tc.Input) != expectedInput {
		t.Errorf("expected Input %q, got %q", expectedInput, string(tc.Input))
	}
}

func TestStreamResult_ResponseIncludesUsageFromDone(t *testing.T) {
	sr, events := NewStreamResult(4)

	go func() {
		defer close(events)
		events <- StreamEvent{
			Type:       StreamEventUsage,
			Usage:      &UsageStats{InputTokens: 100, OutputTokens: 50},
			StopReason: "end_turn",
		}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{
			Usage:      UsageStats{InputTokens: 100, OutputTokens: 50},
			StopReason: "end_turn",
		}, nil)
	}()

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("expected InputTokens 100, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("expected OutputTokens 50, got %d", resp.Usage.OutputTokens)
	}
}

func TestStreamResult_ClosingChannelTriggersResponse(t *testing.T) {
	sr, events := NewStreamResult(4)

	go func() {
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "hi"}
		close(events)
		sr.SetResponse(&ChatResponse{Content: "hi"}, nil)
	}()

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("expected Content %q, got %q", "hi", resp.Content)
	}
}

func TestStreamResult_ErrorEventPropagation(t *testing.T) {
	sr, events := NewStreamResult(4)

	streamErr := errors.New("mid-stream failure")
	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "partial"}
		events <- StreamEvent{Type: StreamEventError, Err: streamErr}
		sr.SetResponse(nil, streamErr)
	}()

	var gotError bool
	for ev := range sr.Events {
		if ev.Type == StreamEventError {
			gotError = true
			if !errors.Is(ev.Err, streamErr) {
				t.Errorf("expected error %v, got %v", streamErr, ev.Err)
			}
		}
	}
	if !gotError {
		t.Error("expected to receive an error event")
	}

	resp, err := sr.Response()
	if err == nil {
		t.Fatal("expected Response() to return error")
	}
	if resp != nil {
		t.Error("expected nil response on error")
	}
}

// --------------------------------------------------------------------------
// ParseSSE tests
// --------------------------------------------------------------------------

func TestParseSSE_BasicSingleEvent(t *testing.T) {
	input := "data: {\"text\":\"hello\"}\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != `{"text":"hello"}` {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

func TestParseSSE_MultiLineData(t *testing.T) {
	input := "data: line1\ndata: line2\ndata: line3\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "line1\nline2\nline3" {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

func TestParseSSE_EventField(t *testing.T) {
	input := "event: message_start\ndata: {}\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "message_start" {
		t.Errorf("expected event 'message_start', got %q", events[0].Event)
	}
}

func TestParseSSE_CommentsIgnored(t *testing.T) {
	input := ": this is a comment\ndata: hello\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "hello" {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

func TestParseSSE_DoneSentinel(t *testing.T) {
	input := "data: [DONE]\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "[DONE]" {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	input := "data: first\n\ndata: second\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if string(events[0].Data) != "first" {
		t.Errorf("event[0] data: %q", string(events[0].Data))
	}
	if string(events[1].Data) != "second" {
		t.Errorf("event[1] data: %q", string(events[1].Data))
	}
}

func TestParseSSE_EmptyLinesOnly(t *testing.T) {
	input := "\n\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestParseSSE_TrailingEventWithoutEmptyLine(t *testing.T) {
	input := "data: trailing"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "trailing" {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

func TestParseSSE_OnEventErrorStopsProcessing(t *testing.T) {
	input := "data: first\n\ndata: second\n\n"
	callbackErr := errors.New("stop processing")
	var count int

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		count++
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("expected callback error, got: %v", err)
	}
	if count != 1 {
		t.Errorf("expected callback called once, got %d", count)
	}
}

func TestParseSSE_ReaderError(t *testing.T) {
	readerErr := errors.New("connection reset")
	r := &failingReader{err: readerErr}

	err := ParseSSE(r, func(ev SSEEvent) error {
		return nil
	})
	// bufio.Scanner wraps reader errors.
	if err == nil {
		t.Fatal("expected error from reader")
	}
}

func TestParseSSE_DataWithoutSpace(t *testing.T) {
	// "data:hello" (no space after colon) should still parse.
	input := "data:hello\n\n"
	var events []SSEEvent

	err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "hello" {
		t.Errorf("unexpected data: %q", string(events[0].Data))
	}
}

// --------------------------------------------------------------------------
// ParseSSELine tests
// --------------------------------------------------------------------------

func TestParseSSELine_EventField(t *testing.T) {
	event, data, ok := ParseSSELine([]byte("event: message_start"))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if event != "message_start" {
		t.Errorf("expected event 'message_start', got %q", event)
	}
	if data != "" {
		t.Errorf("expected empty data, got %q", data)
	}
}

func TestParseSSELine_DataField(t *testing.T) {
	event, data, ok := ParseSSELine([]byte(`data: {"hello":"world"}`))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if event != "" {
		t.Errorf("expected empty event, got %q", event)
	}
	if data != `{"hello":"world"}` {
		t.Errorf("unexpected data: %q", data)
	}
}

func TestParseSSELine_Comment(t *testing.T) {
	_, _, ok := ParseSSELine([]byte(": keep-alive"))
	if !ok {
		t.Fatal("expected ok=true for comment")
	}
}

func TestParseSSELine_InvalidLine(t *testing.T) {
	_, _, ok := ParseSSELine([]byte("not-an-sse-field"))
	if ok {
		t.Error("expected ok=false for non-SSE line")
	}
}

// --------------------------------------------------------------------------
// syncToStream tests
// --------------------------------------------------------------------------

func TestSyncToStream_TextOnly(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		chatResp: &ChatResponse{
			Content:    "Hello world",
			Usage:      UsageStats{InputTokens: 10, OutputTokens: 5},
			StopReason: "end_turn",
		},
	}

	sr, err := syncToStream(context.Background(), mock, ChatRequest{})
	if err != nil {
		t.Fatalf("syncToStream error: %v", err)
	}

	var textDeltas []string
	var gotUsage bool
	var gotDone bool

	for ev := range sr.Events {
		switch ev.Type {
		case StreamEventTextDelta:
			textDeltas = append(textDeltas, ev.Text)
		case StreamEventUsage:
			gotUsage = true
			if ev.Usage.InputTokens != 10 || ev.Usage.OutputTokens != 5 {
				t.Errorf("unexpected usage: %+v", ev.Usage)
			}
			if ev.StopReason != "end_turn" {
				t.Errorf("unexpected stop reason: %q", ev.StopReason)
			}
		case StreamEventDone:
			gotDone = true
		}
	}

	if len(textDeltas) != 1 || textDeltas[0] != "Hello world" {
		t.Errorf("expected single text delta 'Hello world', got %v", textDeltas)
	}
	if !gotUsage {
		t.Error("expected Usage event")
	}
	if !gotDone {
		t.Error("expected Done event")
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Hello world" {
		t.Errorf("expected Content %q, got %q", "Hello world", resp.Content)
	}
}

func TestSyncToStream_WithToolCalls(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		chatResp: &ChatResponse{
			Content: "Let me search",
			ToolCalls: []ToolCall{{
				ID:    "call_1",
				Name:  "search",
				Input: json.RawMessage(`{"q":"test"}`),
			}},
			Usage:      UsageStats{InputTokens: 20, OutputTokens: 10},
			StopReason: "tool_use",
		},
	}

	sr, err := syncToStream(context.Background(), mock, ChatRequest{})
	if err != nil {
		t.Fatalf("syncToStream error: %v", err)
	}

	var eventTypes []StreamEventType
	for ev := range sr.Events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Expected sequence: TextDelta, ToolCallStart, ToolCallDelta, ToolCallEnd, Usage, Done
	expected := []StreamEventType{
		StreamEventTextDelta,
		StreamEventToolCallStart,
		StreamEventToolCallDelta,
		StreamEventToolCallEnd,
		StreamEventUsage,
		StreamEventDone,
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, et := range expected {
		if eventTypes[i] != et {
			t.Errorf("event[%d]: expected %v, got %v", i, et, eventTypes[i])
		}
	}
}

func TestSyncToStream_ChatError(t *testing.T) {
	chatErr := fmt.Errorf("%w: server error", ErrUnavailable)
	mock := &mockProvider{
		name:    "test",
		chatErr: chatErr,
	}

	sr, err := syncToStream(context.Background(), mock, ChatRequest{})
	if err != nil {
		t.Fatalf("syncToStream should not return pre-stream error: %v", err)
	}

	var gotError bool
	for ev := range sr.Events {
		if ev.Type == StreamEventError {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected Error event")
	}

	_, err = sr.Response()
	if err == nil {
		t.Fatal("expected Response() to return error")
	}
}

// --------------------------------------------------------------------------
// StreamEventType.String() tests
// --------------------------------------------------------------------------

func TestStreamEventType_String(t *testing.T) {
	tests := []struct {
		t    StreamEventType
		want string
	}{
		{StreamEventTextDelta, "TextDelta"},
		{StreamEventToolCallStart, "ToolCallStart"},
		{StreamEventToolCallDelta, "ToolCallDelta"},
		{StreamEventToolCallEnd, "ToolCallEnd"},
		{StreamEventUsage, "Usage"},
		{StreamEventDone, "Done"},
		{StreamEventError, "Error"},
		{StreamEventType(99), "StreamEventType(99)"},
	}

	for _, tt := range tests {
		if got := tt.t.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.t, got, tt.want)
		}
	}
}

// --------------------------------------------------------------------------
// StreamEventReasoningDelta tests (Phase 1.1)
// --------------------------------------------------------------------------

func TestStreamEventType_ReasoningDelta(t *testing.T) {
	tests := []struct {
		name      string
		eventType StreamEventType
		wantStr   string
		wantIota  int
	}{
		{
			name:      "ReasoningDelta string representation",
			eventType: StreamEventReasoningDelta,
			wantStr:   "ReasoningDelta",
		},
		{
			name:      "TextDelta is iota 0",
			eventType: StreamEventTextDelta,
			wantIota:  0,
		},
		{
			name:      "ReasoningDelta is iota 1",
			eventType: StreamEventReasoningDelta,
			wantIota:  1,
		},
		{
			name:      "ToolCallStart is iota 2",
			eventType: StreamEventToolCallStart,
			wantIota:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStr != "" {
				if got := tt.eventType.String(); got != tt.wantStr {
					t.Errorf("String() = %q, want %q", got, tt.wantStr)
				}
			}
			if tt.wantIota != 0 || tt.name == "TextDelta is iota 0" {
				if int(tt.eventType) != tt.wantIota {
					t.Errorf("iota value = %d, want %d", int(tt.eventType), tt.wantIota)
				}
			}
		})
	}
}

// --------------------------------------------------------------------------
// assembleToolCall test
// --------------------------------------------------------------------------

func TestAssembleToolCall(t *testing.T) {
	tc := assembleToolCall("id_1", "shell", `{"cmd":"ls"}`)
	if tc.ID != "id_1" || tc.Name != "shell" {
		t.Errorf("unexpected ID/Name: %+v", tc)
	}
	if string(tc.Input) != `{"cmd":"ls"}` {
		t.Errorf("unexpected Input: %q", string(tc.Input))
	}
}

// --------------------------------------------------------------------------
// ModelInfo.SupportedParameters tests (Phase 1.2)
// --------------------------------------------------------------------------

func TestModelInfo_SupportedParameters_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input ModelInfo
		json  string
	}{
		{
			name: "SupportedParameters present",
			input: ModelInfo{
				ID:                  "model-x",
				Name:                "Model X",
				SupportedParameters: []string{"reasoning", "include_reasoning"},
			},
			json: `{"id":"model-x","name":"Model X","context_length":0,"prompt_cost":0,"completion_cost":0,"free":false,"supported_parameters":["reasoning","include_reasoning"]}`,
		},
		{
			name: "SupportedParameters absent omitted",
			input: ModelInfo{
				ID:   "model-y",
				Name: "Model Y",
			},
			// omitempty: field should be absent from JSON output
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}
			if tt.json != "" {
				if string(b) != tt.json {
					t.Errorf("Marshal = %s, want %s", string(b), tt.json)
				}
			}
			// For absent case: verify field is not in output
			if tt.input.SupportedParameters == nil {
				if strings.Contains(string(b), "supported_parameters") {
					t.Errorf("expected 'supported_parameters' to be omitted, got: %s", string(b))
				}
			}

			// Round-trip
			var got ModelInfo
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}
			if len(got.SupportedParameters) != len(tt.input.SupportedParameters) {
				t.Errorf("SupportedParameters length = %d, want %d", len(got.SupportedParameters), len(tt.input.SupportedParameters))
			}
			for i, v := range tt.input.SupportedParameters {
				if got.SupportedParameters[i] != v {
					t.Errorf("SupportedParameters[%d] = %q, want %q", i, got.SupportedParameters[i], v)
				}
			}
		})
	}
}

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

// failingReader returns an error after a few bytes.
type failingReader struct {
	err    error
	called bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.called {
		return 0, r.err
	}
	r.called = true
	// Return some data first so the scanner attempts to read more.
	copy(p, []byte("data: partial\n"))
	return len("data: partial\n"), nil
}

// Verify interface compliance at compile time.
var _ io.Reader = (*failingReader)(nil)
var _ io.Reader = (*bytes.Buffer)(nil)
