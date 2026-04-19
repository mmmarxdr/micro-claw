package web

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"daimon/internal/config"
	"daimon/internal/provider"
)

// modelListerRegistry is the minimal interface needed by validateConfiguredModel.
// *provider.Registry satisfies this.
type modelListerRegistry interface {
	Lister(name string) (provider.ModelLister, bool)
}

// ValidateConfiguredModel checks that cfg.Models.Default.Model exists in the
// provider's live model list.
//
// Rules:
//   - Provider not in registry (no credentials) → skip silently, return nil.
//   - ListModels() fails (network issue, 5xx) → log warning, return nil (non-blocking).
//   - Model not found → log warning with top-5 suggestion list, return nil.
//   - Model found → return nil (no-op, healthy path).
//
// This function is intentionally warn-only: a transient upstream failure MUST NOT
// prevent the server from starting.
//
// Exported for use from cmd/daimon/main.go (web package is the natural owner).
func ValidateConfiguredModel(ctx context.Context, reg modelListerRegistry, cfg config.Config) error {
	return validateConfiguredModel(ctx, reg, cfg)
}

func validateConfiguredModel(ctx context.Context, reg modelListerRegistry, cfg config.Config) error {
	providerName := cfg.Models.Default.Provider
	modelID := cfg.Models.Default.Model

	if providerName == "" || modelID == "" {
		return nil
	}

	lister, ok := reg.Lister(providerName)
	if !ok {
		// Provider has no credentials configured — skip.
		return nil
	}

	models, err := lister.ListModels(ctx)
	if err != nil {
		slog.Warn("startup model validation: ListModels failed, skipping check",
			"provider", providerName, "error", err)
		return nil
	}

	for _, m := range models {
		if m.ID == modelID {
			// Model found — all good.
			return nil
		}
	}

	// Model not found — build a top-5 suggestion list for the user.
	top := models
	if len(top) > 5 {
		top = top[:5]
	}
	ids := make([]string, 0, len(top))
	for _, m := range top {
		ids = append(ids, m.ID)
	}

	slog.Warn(
		fmt.Sprintf(`[daimon] WARNING: model %q not found in provider %q live list — run daimon config to update`, modelID, providerName),
		"suggestion", strings.Join(ids, ", "),
	)
	return nil
}
