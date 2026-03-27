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

// memoryHitCount returns the number of distinct keywords from kws that appear
// in the lowercased content or tags of entry.
func memoryHitCount(entry MemoryEntry, kws []string) int {
	lowerContent := strings.ToLower(entry.Content)
	lowerTags := make([]string, len(entry.Tags))
	for i, t := range entry.Tags {
		lowerTags[i] = strings.ToLower(t)
	}

	count := 0
	for _, kw := range kws {
		if strings.Contains(lowerContent, kw) {
			count++
			continue
		}
		for _, lt := range lowerTags {
			if strings.Contains(lt, kw) {
				count++
				break
			}
		}
	}
	return count
}

// SearchMemory returns memory entries in scopeID that match the given query.
//
// Keywords are extracted from query (stop words removed). Each entry is scored
// by the number of keywords it contains. Results are sorted by (hit count DESC,
// created_at DESC) so the most relevant and most recent entries come first.
// If query is empty, all entries are returned sorted by created_at DESC.
func (s *FileStore) SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error) {
	entries, err := s.loadMemory(scopeID)
	if err != nil {
		return nil, err
	}

	if query == "" {
		// No query: return all entries newest-first.
		result := make([]MemoryEntry, len(entries))
		copy(result, entries)
		sort.Slice(result, func(i, j int) bool {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		})
		if limit > 0 && len(result) > limit {
			result = result[:limit]
		}
		return result, nil
	}

	keywords := ExtractKeywords(query)

	// If all tokens were stop words, fall back to plain substring match with
	// the original lowercased query so behaviour degrades gracefully.
	useFallback := len(keywords) == 0
	lowerQuery := strings.ToLower(query)

	type scored struct {
		entry MemoryEntry
		hits  int
	}
	var candidates []scored

	for _, e := range entries {
		var hits int
		if useFallback {
			// Substring fallback: treat as 1-hit match if found.
			lc := strings.ToLower(e.Content)
			found := strings.Contains(lc, lowerQuery)
			if !found {
				for _, tag := range e.Tags {
					if strings.Contains(strings.ToLower(tag), lowerQuery) {
						found = true
						break
					}
				}
			}
			if found {
				hits = 1
			}
		} else {
			hits = memoryHitCount(e, keywords)
		}
		if hits > 0 {
			candidates = append(candidates, scored{entry: e, hits: hits})
		}
	}

	// Sort by: hit count DESC, then created_at DESC.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].hits != candidates[j].hits {
			return candidates[i].hits > candidates[j].hits
		}
		return candidates[i].entry.CreatedAt.After(candidates[j].entry.CreatedAt)
	})

	result := make([]MemoryEntry, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.entry)
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
