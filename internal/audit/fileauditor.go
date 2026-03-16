package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileAuditor writes audit events as newline-delimited JSON (JSONL) to per-scope files.
// Files are named audit_{sanitized_scope_id}.jsonl inside basePath.
// File handles are cached for the lifetime of the auditor to avoid repeated open() syscalls.
type FileAuditor struct {
	basePath string
	mu       sync.Mutex
	handles  map[string]*os.File
}

// NewFileAuditor creates a FileAuditor, ensuring basePath exists.
func NewFileAuditor(basePath string) (*FileAuditor, error) {
	if err := os.MkdirAll(basePath, 0o750); err != nil {
		return nil, fmt.Errorf("audit: failed to create directory %q: %w", basePath, err)
	}
	return &FileAuditor{
		basePath: basePath,
		handles:  make(map[string]*os.File),
	}, nil
}

// Emit serialises event to JSON and appends it as a single line to the scope-specific file.
func (a *FileAuditor) Emit(_ context.Context, event AuditEvent) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: marshal event: %w", err)
	}

	f, err := a.handle(event.ScopeID)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// Close closes all open file handles.
func (a *FileAuditor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var firstErr error
	for _, f := range a.handles {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	a.handles = make(map[string]*os.File)
	return firstErr
}

// handle returns (or creates) the cached file handle for the given scopeID.
func (a *FileAuditor) handle(scopeID string) (*os.File, error) {
	sanitized := sanitizeScope(scopeID)
	name := filepath.Join(a.basePath, "audit_"+sanitized+".jsonl")

	a.mu.Lock()
	f, ok := a.handles[sanitized]
	a.mu.Unlock()
	if ok {
		return f, nil
	}

	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("audit: open %q: %w", name, err)
	}

	a.mu.Lock()
	a.handles[sanitized] = f
	a.mu.Unlock()
	return f, nil
}

// sanitizeScope replaces characters that are invalid in filenames with underscores.
func sanitizeScope(scopeID string) string {
	r := strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_")
	return r.Replace(scopeID)
}
