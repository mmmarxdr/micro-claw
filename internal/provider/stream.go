package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// --------------------------------------------------------------------------
// StreamEventType — identifies the kind of streaming event
// --------------------------------------------------------------------------

// StreamEventType identifies the kind of streaming event.
type StreamEventType int

const (
	// StreamEventTextDelta delivers a partial text fragment from the LLM.
	StreamEventTextDelta StreamEventType = iota

	// StreamEventReasoningDelta delivers a partial reasoning/thinking token fragment.
	// Emitted by providers that support extended thinking (Anthropic thinking blocks,
	// OpenRouter reasoning fields). Not accumulated into ChatResponse.Content.
	StreamEventReasoningDelta

	// StreamEventToolCallStart signals a new tool call block. Carries ToolCallID and ToolName.
	StreamEventToolCallStart

	// StreamEventToolCallDelta delivers a partial JSON fragment of tool call input.
	StreamEventToolCallDelta

	// StreamEventToolCallEnd signals the tool call block is complete.
	// The full tool call can be reconstructed from Start + accumulated Deltas.
	StreamEventToolCallEnd

	// StreamEventUsage carries token usage and stop reason metadata.
	StreamEventUsage

	// StreamEventDone signals the stream has completed normally.
	// No more events will be sent after this.
	StreamEventDone

	// StreamEventError signals an error occurred mid-stream.
	// Err is populated. No more events will be sent after this.
	StreamEventError
)

// --------------------------------------------------------------------------
// StreamEvent — a single event from a streaming LLM response
// --------------------------------------------------------------------------

// StreamEvent is a single event from a streaming LLM response.
type StreamEvent struct {
	Type       StreamEventType
	Text       string      // StreamEventTextDelta: the text fragment
	ToolCallID string      // StreamEventToolCallStart: the tool call ID assigned by the LLM
	ToolName   string      // StreamEventToolCallStart: the tool name
	ToolInput  string      // StreamEventToolCallDelta: partial JSON fragment of tool call input
	Usage      *UsageStats // StreamEventUsage: token counts
	StopReason string      // StreamEventUsage: stop reason from the model
	Err        error       // StreamEventError: the error that occurred
}

// --------------------------------------------------------------------------
// StreamResult — wraps a streaming response with assembled result access
// --------------------------------------------------------------------------

// StreamResult wraps a streaming response. Callers consume Events for
// progressive display, then call Response() to get the fully assembled result.
type StreamResult struct {
	// Events delivers streaming events. Closed after the terminal event (Done or Error).
	Events <-chan StreamEvent

	// done is closed when the provider goroutine finishes assembling the response.
	done chan struct{}
	resp *ChatResponse
	err  error
	once sync.Once
}

// Response blocks until the stream is complete and returns the assembled response.
// The caller MUST drain Events before calling Response, or Response will drain
// them internally (events are lost but no deadlock).
func (r *StreamResult) Response() (*ChatResponse, error) {
	// Drain any remaining events to prevent goroutine leak.
	for range r.Events {
	}
	<-r.done
	return r.resp, r.err
}

// NewStreamResult creates a StreamResult with a buffered event channel.
// It returns a read-only StreamResult (for the consumer) and a write-only
// channel (for the producer). The producer MUST close done after setting
// resp/err and closing the events channel.
func NewStreamResult(bufSize int) (*StreamResult, chan<- StreamEvent) {
	ch := make(chan StreamEvent, bufSize)
	sr := &StreamResult{
		Events: ch,
		done:   make(chan struct{}),
	}
	return sr, ch
}

// SetResponse stores the final assembled response on the StreamResult
// and signals completion. Must be called exactly once by the producer,
// after closing the events channel.
func (r *StreamResult) SetResponse(resp *ChatResponse, err error) {
	r.once.Do(func() {
		r.resp = resp
		r.err = err
		close(r.done)
	})
}

// --------------------------------------------------------------------------
// StreamingProvider — interface for providers that support streaming
// --------------------------------------------------------------------------

// StreamingProvider is an optional interface for providers that support
// server-sent event (SSE) streaming. Checked via type assertion.
type StreamingProvider interface {
	Provider
	ChatStream(ctx context.Context, req ChatRequest) (*StreamResult, error)
}

// --------------------------------------------------------------------------
// SSE parsing — shared utility for all SSE-based providers
// --------------------------------------------------------------------------

// SSEEvent represents a single parsed SSE frame.
type SSEEvent struct {
	Event string // the "event:" field (empty if not present)
	Data  []byte // the "data:" field content (may span multiple lines)
}

// ParseSSE reads SSE events from r and calls onEvent for each complete event.
// It handles:
//   - Multi-line "data:" fields (concatenated with newline)
//   - "event:" field (optional, provider-specific)
//   - Comment lines (": ...") — ignored
//   - Empty lines as event delimiters
//   - "[DONE]" sentinel for OpenAI-format streams
//
// Returns nil on clean EOF, or the first error from onEvent/read.
func ParseSSE(r io.Reader, onEvent func(ev SSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	var currentEvent SSEEvent
	var dataLines [][]byte

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line = end of event frame.
			if len(dataLines) > 0 {
				currentEvent.Data = bytes.Join(dataLines, []byte("\n"))
				if err := onEvent(currentEvent); err != nil {
					return err
				}
			}
			currentEvent = SSEEvent{}
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, ":") {
			continue // SSE comment
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ") // optional space after "data:"
			dataLines = append(dataLines, []byte(data))
		}
		// Ignore other fields (id:, retry:) — not used by any of our providers.
	}

	// Handle trailing event without final empty line.
	if len(dataLines) > 0 {
		currentEvent.Data = bytes.Join(dataLines, []byte("\n"))
		if err := onEvent(currentEvent); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// --------------------------------------------------------------------------
// syncToStream — wraps a synchronous Chat() call into a StreamResult
// --------------------------------------------------------------------------

// syncToStream wraps a synchronous Provider.Chat() call into a StreamResult
// so the agent loop can use a uniform streaming interface.
func syncToStream(ctx context.Context, p Provider, req ChatRequest) (*StreamResult, error) {
	sr, events := NewStreamResult(8)

	go func() {
		defer close(events)

		resp, err := p.Chat(ctx, req)
		if err != nil {
			events <- StreamEvent{Type: StreamEventError, Err: err}
			sr.SetResponse(nil, err)
			return
		}

		// Emit the full text as a single delta.
		if resp.Content != "" {
			events <- StreamEvent{Type: StreamEventTextDelta, Text: resp.Content}
		}

		// Emit tool calls.
		for _, tc := range resp.ToolCalls {
			events <- StreamEvent{
				Type:       StreamEventToolCallStart,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
			}
			events <- StreamEvent{
				Type:      StreamEventToolCallDelta,
				ToolInput: string(tc.Input),
			}
			events <- StreamEvent{Type: StreamEventToolCallEnd}
		}

		// Emit usage and done.
		events <- StreamEvent{
			Type:       StreamEventUsage,
			Usage:      &resp.Usage,
			StopReason: resp.StopReason,
		}
		events <- StreamEvent{Type: StreamEventDone}

		sr.SetResponse(resp, nil)
	}()

	return sr, nil
}

// --------------------------------------------------------------------------
// ParseSSELine — single-line SSE utility (convenience for simple cases)
// --------------------------------------------------------------------------

// ParseSSELine parses a single SSE line and returns the event type, data, and
// whether the line was a valid SSE field. This is a lower-level alternative to
// ParseSSE for cases where the caller manages its own line scanning.
func ParseSSELine(line []byte) (event, data string, ok bool) {
	s := string(line)

	if strings.HasPrefix(s, "event:") {
		return strings.TrimSpace(strings.TrimPrefix(s, "event:")), "", true
	}
	if strings.HasPrefix(s, "data:") {
		d := strings.TrimPrefix(s, "data:")
		d = strings.TrimPrefix(d, " ")
		return "", d, true
	}
	if strings.HasPrefix(s, ":") {
		return "", "", true // comment — valid but no data
	}

	return "", "", false
}

// assembleToolCall is a helper to build a ToolCall from accumulated stream data.
func assembleToolCall(id, name, inputJSON string) ToolCall {
	return ToolCall{
		ID:    id,
		Name:  name,
		Input: json.RawMessage(inputJSON),
	}
}

// --------------------------------------------------------------------------
// Stringer for StreamEventType (useful for logging / debugging)
// --------------------------------------------------------------------------

func (t StreamEventType) String() string {
	switch t {
	case StreamEventTextDelta:
		return "TextDelta"
	case StreamEventReasoningDelta:
		return "ReasoningDelta"
	case StreamEventToolCallStart:
		return "ToolCallStart"
	case StreamEventToolCallDelta:
		return "ToolCallDelta"
	case StreamEventToolCallEnd:
		return "ToolCallEnd"
	case StreamEventUsage:
		return "Usage"
	case StreamEventDone:
		return "Done"
	case StreamEventError:
		return "Error"
	default:
		return fmt.Sprintf("StreamEventType(%d)", t)
	}
}
