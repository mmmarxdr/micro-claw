package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// FallbackProvider is a Provider decorator that transparently routes calls to a
// secondary provider when the primary fails with a rate-limit or availability error.
type FallbackProvider struct {
	primary  Provider
	fallback Provider
	logger   *slog.Logger
}

// NewFallbackProvider constructs a FallbackProvider wrapping primary and fallback.
func NewFallbackProvider(primary, fallback Provider, logger *slog.Logger) *FallbackProvider {
	return &FallbackProvider{primary: primary, fallback: fallback, logger: logger}
}

// isFallbackEligible reports whether err should trigger fallback routing.
// Only rate-limit and availability errors are eligible for fallback.
func isFallbackEligible(err error) bool {
	return errors.Is(err, ErrRateLimit) || errors.Is(err, ErrUnavailable)
}

// --------------------------------------------------------------------------
// Provider interface implementation
// --------------------------------------------------------------------------

// Name returns a composite name in the format "<primary>(<fallback>)".
func (f *FallbackProvider) Name() string {
	return f.primary.Name() + "(" + f.fallback.Name() + ")"
}

// SupportsTools returns true only if BOTH providers support tools.
// Conservative intersection: if the fallback is activated mid-conversation and
// doesn't support tools, an ongoing tool-calling loop would break.
func (f *FallbackProvider) SupportsTools() bool {
	return f.primary.SupportsTools() && f.fallback.SupportsTools()
}

// HealthCheck checks the primary; if unhealthy, falls back to the secondary.
// Returns a combined error if both are unhealthy (triggers os.Exit(1) in main.go).
func (f *FallbackProvider) HealthCheck(ctx context.Context) (string, error) {
	name, err := f.primary.HealthCheck(ctx)
	if err == nil {
		return name, nil
	}
	f.logger.Warn("primary provider unhealthy at startup, checking fallback",
		"primary", f.primary.Name(), "error", err)
	name2, err2 := f.fallback.HealthCheck(ctx)
	if err2 != nil {
		return "", fmt.Errorf("primary: %w; fallback: %v", err, err2)
	}
	f.logger.Warn("startup: primary unhealthy, proceeding with fallback",
		"fallback", f.fallback.Name())
	return name2 + " (via fallback)", nil
}

// Chat calls the primary provider; on eligible errors, transparently routes to fallback.
// Eligible errors: ErrRateLimit, ErrUnavailable.
// Non-eligible errors (ErrAuth, ErrBadRequest, etc.) are returned immediately.
func (f *FallbackProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	resp, err := f.primary.Chat(ctx, req)
	if err == nil {
		return resp, nil
	}
	if !isFallbackEligible(err) {
		return nil, err
	}
	f.logger.Warn("primary provider failed, activating fallback",
		"primary", f.primary.Name(),
		"fallback", f.fallback.Name(),
		"error", err,
	)
	resp2, err2 := f.fallback.Chat(ctx, req)
	if err2 != nil {
		// Preserve primary sentinel with %w so errors.Is() still works on the combined error.
		return nil, fmt.Errorf("primary: %w; fallback: %v", err, err2)
	}
	return resp2, nil
}
