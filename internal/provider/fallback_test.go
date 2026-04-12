package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// mockProvider — test double for Provider interface
// --------------------------------------------------------------------------

type mockProvider struct {
	name               string
	supportsTools      bool
	supportsMultimodal bool
	supportsAudio      bool
	chatResp           *ChatResponse
	chatErr            error
	healthName         string
	healthErr          error
	chatCalled         int
	healthCalled       int
}

func (m *mockProvider) Name() string             { return m.name }
func (m *mockProvider) SupportsTools() bool      { return m.supportsTools }
func (m *mockProvider) SupportsMultimodal() bool { return m.supportsMultimodal }
func (m *mockProvider) SupportsAudio() bool      { return m.supportsAudio }

func (m *mockProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	m.chatCalled++
	return m.chatResp, m.chatErr
}

func (m *mockProvider) HealthCheck(_ context.Context) (string, error) {
	m.healthCalled++
	return m.healthName, m.healthErr
}

// newFallback is a convenience constructor for tests.
func newFallback(primary, fb Provider) *FallbackProvider {
	return NewFallbackProvider(primary, fb, slog.Default())
}

// --------------------------------------------------------------------------
// T1: Happy path — primary succeeds, fallback never called
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_HappyPath(t *testing.T) {
	primary := &mockProvider{
		name:     "primary",
		chatResp: &ChatResponse{Content: "ok"},
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	resp, err := f.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want ok", resp.Content)
	}
	if fallback.chatCalled != 0 {
		t.Errorf("fallback.chatCalled = %d, want 0", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T2: Rate limit — fallback succeeds
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_RateLimit_FallbackSucceeds(t *testing.T) {
	primary := &mockProvider{
		name:    "primary",
		chatErr: fmt.Errorf("too many: %w", ErrRateLimit),
	}
	fallback := &mockProvider{
		name:     "fallback",
		chatResp: &ChatResponse{Content: "fallback-ok"},
	}
	f := newFallback(primary, fallback)

	resp, err := f.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "fallback-ok" {
		t.Errorf("Content = %q, want fallback-ok", resp.Content)
	}
	if fallback.chatCalled != 1 {
		t.Errorf("fallback.chatCalled = %d, want 1", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T3: Unavailable — fallback succeeds
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_Unavailable_FallbackSucceeds(t *testing.T) {
	primary := &mockProvider{
		name:    "primary",
		chatErr: fmt.Errorf("server down: %w", ErrUnavailable),
	}
	fallback := &mockProvider{
		name:     "fallback",
		chatResp: &ChatResponse{Content: "fallback-ok"},
	}
	f := newFallback(primary, fallback)

	resp, err := f.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "fallback-ok" {
		t.Errorf("Content = %q, want fallback-ok", resp.Content)
	}
}

// --------------------------------------------------------------------------
// T4: Auth error — fallback NOT called, primary error returned
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_AuthError_NoFallback(t *testing.T) {
	primary := &mockProvider{
		name:    "primary",
		chatErr: fmt.Errorf("bad key: %w", ErrAuth),
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	_, err := f.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("errors.Is(err, ErrAuth) = false; err = %v", err)
	}
	if fallback.chatCalled != 0 {
		t.Errorf("fallback.chatCalled = %d, want 0 (no fallback on auth error)", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T5: BadRequest error — fallback NOT called
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_BadRequest_NoFallback(t *testing.T) {
	primary := &mockProvider{
		name:    "primary",
		chatErr: fmt.Errorf("bad body: %w", ErrBadRequest),
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	_, err := f.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if fallback.chatCalled != 0 {
		t.Errorf("fallback.chatCalled = %d, want 0 (no fallback on bad request)", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T6: Both fail — combined error preserves primary sentinel
// --------------------------------------------------------------------------

func TestFallbackProvider_Chat_BothFail(t *testing.T) {
	primaryErr := fmt.Errorf("too many: %w", ErrRateLimit)
	primary := &mockProvider{
		name:    "primary",
		chatErr: primaryErr,
	}
	fallback := &mockProvider{
		name:    "fallback",
		chatErr: fmt.Errorf("also down"),
	}
	f := newFallback(primary, fallback)

	_, err := f.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Primary sentinel must be preserved in combined error.
	if !errors.Is(err, ErrRateLimit) {
		t.Errorf("errors.Is(err, ErrRateLimit) = false; err = %v", err)
	}
	// Combined error message should mention both.
	if !strings.Contains(err.Error(), "primary:") {
		t.Errorf("error missing 'primary:' prefix; err = %v", err)
	}
	if !strings.Contains(err.Error(), "fallback:") {
		t.Errorf("error missing 'fallback:' suffix; err = %v", err)
	}
}

// --------------------------------------------------------------------------
// T7: HealthCheck — primary healthy, fallback never called
// --------------------------------------------------------------------------

func TestFallbackProvider_HealthCheck_PrimaryHealthy(t *testing.T) {
	primary := &mockProvider{name: "primary", healthName: "model-a"}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	name, err := f.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
	if name != "model-a" {
		t.Errorf("HealthCheck() = %q, want model-a", name)
	}
	if fallback.healthCalled != 0 {
		t.Errorf("fallback.healthCalled = %d, want 0", fallback.healthCalled)
	}
}

// --------------------------------------------------------------------------
// T8: HealthCheck — primary fails, fallback healthy → "(via fallback)"
// --------------------------------------------------------------------------

func TestFallbackProvider_HealthCheck_PrimaryFails_FallbackHealthy(t *testing.T) {
	primary := &mockProvider{
		name:      "primary",
		healthErr: fmt.Errorf("primary is down"),
	}
	fallback := &mockProvider{name: "fallback", healthName: "model-b"}
	f := newFallback(primary, fallback)

	name, err := f.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
	if !strings.Contains(name, "(via fallback)") {
		t.Errorf("HealthCheck() = %q, want it to contain '(via fallback)'", name)
	}
}

// --------------------------------------------------------------------------
// T9: HealthCheck — both fail → combined error
// --------------------------------------------------------------------------

func TestFallbackProvider_HealthCheck_BothFail(t *testing.T) {
	primary := &mockProvider{
		name:      "primary",
		healthErr: fmt.Errorf("primary down"),
	}
	fallback := &mockProvider{
		name:      "fallback",
		healthErr: fmt.Errorf("fallback down too"),
	}
	f := newFallback(primary, fallback)

	_, err := f.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error when both providers are unhealthy")
	}
}

// --------------------------------------------------------------------------
// T10: SupportsTools — both true
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsTools_BothTrue(t *testing.T) {
	primary := &mockProvider{name: "p", supportsTools: true}
	fallback := &mockProvider{name: "f", supportsTools: true}
	f := newFallback(primary, fallback)

	if !f.SupportsTools() {
		t.Error("SupportsTools() = false, want true when both support tools")
	}
}

// --------------------------------------------------------------------------
// T11: SupportsTools — primary true, fallback false → false
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsTools_PrimaryTrue_FallbackFalse(t *testing.T) {
	primary := &mockProvider{name: "p", supportsTools: true}
	fallback := &mockProvider{name: "f", supportsTools: false}
	f := newFallback(primary, fallback)

	if f.SupportsTools() {
		t.Error("SupportsTools() = true, want false when fallback doesn't support tools")
	}
}

// --------------------------------------------------------------------------
// T12: SupportsTools — both false
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsTools_BothFalse(t *testing.T) {
	primary := &mockProvider{name: "p", supportsTools: false}
	fallback := &mockProvider{name: "f", supportsTools: false}
	f := newFallback(primary, fallback)

	if f.SupportsTools() {
		t.Error("SupportsTools() = true, want false when both don't support tools")
	}
}

// --------------------------------------------------------------------------
// T13: Name format
// --------------------------------------------------------------------------

func TestFallbackProvider_Name(t *testing.T) {
	primary := &mockProvider{name: "gemini"}
	fallback := &mockProvider{name: "openrouter"}
	f := newFallback(primary, fallback)

	got := f.Name()
	if got != "gemini(openrouter)" {
		t.Errorf("Name() = %q, want gemini(openrouter)", got)
	}
}

// --------------------------------------------------------------------------
// T14: SupportsMultimodal + SupportsAudio — mixed pool (one text-only) → false
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsMultimodal_Mixed(t *testing.T) {
	primary := &mockProvider{name: "multimodal", supportsMultimodal: true, supportsAudio: true}
	fallback := &mockProvider{name: "textonly", supportsMultimodal: false, supportsAudio: false}
	f := newFallback(primary, fallback)

	if f.SupportsMultimodal() {
		t.Error("SupportsMultimodal() = true, want false when one member is text-only")
	}
	if f.SupportsAudio() {
		t.Error("SupportsAudio() = true, want false when one member is text-only")
	}
}

// --------------------------------------------------------------------------
// T15: SupportsMultimodal + SupportsAudio — all multimodal → true
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsMultimodal_AllMultimodal(t *testing.T) {
	primary := &mockProvider{name: "a", supportsMultimodal: true, supportsAudio: true}
	fallback := &mockProvider{name: "b", supportsMultimodal: true, supportsAudio: true}
	f := newFallback(primary, fallback)

	if !f.SupportsMultimodal() {
		t.Error("SupportsMultimodal() = false, want true when all members support multimodal")
	}
	if !f.SupportsAudio() {
		t.Error("SupportsAudio() = false, want true when all members support audio")
	}
}

// --------------------------------------------------------------------------
// T16: SupportsMultimodal + SupportsAudio — nil member (empty-pool guard) → false
// --------------------------------------------------------------------------

func TestFallbackProvider_SupportsMultimodal_NilMember(t *testing.T) {
	// Simulate "empty pool" by constructing a FallbackProvider with nil members.
	// The nil guard MUST return false rather than panicking.
	f := &FallbackProvider{primary: nil, fallback: nil, logger: slog.Default()}

	if f.SupportsMultimodal() {
		t.Error("SupportsMultimodal() = true on nil-member FallbackProvider, want false")
	}
	if f.SupportsAudio() {
		t.Error("SupportsAudio() = true on nil-member FallbackProvider, want false")
	}
}
