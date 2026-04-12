package provider

import "context"

// mediaReader is the minimal interface the Anthropic (and future OpenAI/Gemini)
// request builders need to load image bytes from the backing store.
//
// Phase 4's store.MediaStore will satisfy this interface automatically via Go's
// structural typing — no import cycle, no coupling. Phase 3 tests use an inline
// stub that implements this interface directly.
//
// If the provider's media field is nil (text-only path, no store wired yet),
// the builder falls back gracefully to FlattenBlocks / placeholder text.
type mediaReader interface {
	GetMedia(ctx context.Context, sha256 string) ([]byte, string, error)
}
