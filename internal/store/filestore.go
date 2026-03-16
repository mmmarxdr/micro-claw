package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"microagent/internal/config"
)

type FileStore struct {
	config config.StoreConfig
}

func NewFileStore(cfg config.StoreConfig) *FileStore {
	return &FileStore{config: cfg}
}

func (s *FileStore) Close() error {
	return nil
}

func (s *FileStore) convPath(id string) (string, error) {
	basePath := s.config.Path
	if basePath == "" {
		basePath = "~/.microagent/data"
	}
	if strings.HasPrefix(basePath, "~") {
		if usr, err := os.UserHomeDir(); err == nil {
			basePath = strings.Replace(basePath, "~", usr, 1)
		}
	}
	dir := filepath.Join(basePath, "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	id = filepath.Base(filepath.Clean(id))
	return filepath.Join(dir, id+".json"), nil
}

func (s *FileStore) memPath(scopeID string) (string, error) {
	basePath := s.config.Path
	if basePath == "" {
		basePath = "~/.microagent/data"
	}
	if strings.HasPrefix(basePath, "~") {
		if usr, err := os.UserHomeDir(); err == nil {
			basePath = strings.Replace(basePath, "~", usr, 1)
		}
	}
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return "", err
	}
	if scopeID == "" {
		scopeID = "global"
	}
	// Sanitize scopeID against directory traversal
	scopeID = filepath.Base(filepath.Clean(scopeID))
	return filepath.Join(basePath, "memory_"+scopeID+".json"), nil
}

func (s *FileStore) atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *FileStore) SaveConversation(ctx context.Context, conv Conversation) error {
	path, err := s.convPath(conv.ID)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return err
	}

	return s.atomicWrite(path, data)
}

func (s *FileStore) LoadConversation(ctx context.Context, id string) (*Conversation, error) {
	path, err := s.convPath(id)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("loading conversation %s: %w", id, ErrNotFound)
		}
		return nil, err
	}

	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, err
	}

	return &conv, nil
}

func (s *FileStore) ListConversations(ctx context.Context, channelID string, limit int) ([]Conversation, error) {
	path, err := s.convPath("")
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var convs []Conversation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil {
			var conv Conversation
			if err := json.Unmarshal(data, &conv); err == nil {
				if channelID == "" || conv.ChannelID == channelID {
					convs = append(convs, conv)
				}
			}
		}
	}

	sort.Slice(convs, func(i, j int) bool {
		return convs[i].UpdatedAt.After(convs[j].UpdatedAt)
	})

	if limit > 0 && len(convs) > limit {
		convs = convs[:limit]
	}

	return convs, nil
}

func (s *FileStore) loadMemory(scopeID string) ([]MemoryEntry, error) {
	path, err := s.memPath(scopeID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []MemoryEntry{}, nil
		}
		return nil, err
	}

	var entries []MemoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}

	return entries, nil
}

func (s *FileStore) saveMemory(scopeID string, entries []MemoryEntry) error {
	path, err := s.memPath(scopeID)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	return s.atomicWrite(path, data)
}

func (s *FileStore) AppendMemory(ctx context.Context, scopeID string, entry MemoryEntry) error {
	entries, err := s.loadMemory(scopeID)
	if err != nil {
		return err
	}

	entries = append(entries, entry)
	return s.saveMemory(scopeID, entries)
}

func (s *FileStore) SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error) {
	entries, err := s.loadMemory(scopeID)
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var matches []MemoryEntry

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		match := false
		if strings.Contains(strings.ToLower(e.Content), query) {
			match = true
		} else {
			for _, tag := range e.Tags {
				if strings.Contains(strings.ToLower(tag), query) {
					match = true
					break
				}
			}
		}

		if match {
			matches = append(matches, e)
			if limit > 0 && len(matches) >= limit {
				break
			}
		}
	}

	return matches, nil
}
