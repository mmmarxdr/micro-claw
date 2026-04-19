package channel

import (
	"context"
	"io"
)

// BeginStream starts a streaming response on the CLI channel.
// Tokens are written directly to the output writer (typically os.Stdout),
// which is unbuffered, so each chunk appears immediately.
func (c *CLIChannel) BeginStream(ctx context.Context, channelID string) (StreamWriter, error) {
	return &cliStreamWriter{w: c.out}, nil
}

// cliStreamWriter writes streamed text chunks to an io.Writer.
type cliStreamWriter struct {
	w io.Writer
}

// WriteChunk sends a partial text fragment to the output.
func (sw *cliStreamWriter) WriteChunk(text string) error {
	_, err := sw.w.Write([]byte(text))
	return err
}

// WriteReasoning is a no-op for CLI — reasoning tokens are not surfaced in terminal output.
func (sw *cliStreamWriter) WriteReasoning(_ string) error { return nil }

// Finalize marks the stream as complete by writing a trailing newline.
func (sw *cliStreamWriter) Finalize() error {
	_, err := sw.w.Write([]byte("\n"))
	return err
}

// Abort terminates the stream, writing a newline to keep the terminal clean.
func (sw *cliStreamWriter) Abort(err error) error {
	_, writeErr := sw.w.Write([]byte("\n"))
	return writeErr
}
