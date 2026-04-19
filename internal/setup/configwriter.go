package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"daimon/internal/config"
)

// DefaultConfigPath returns the canonical config file path for the current user.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("configwriter: get home dir: %w", err)
	}
	return filepath.Join(home, ".microagent", "config.yaml"), nil
}

// DetectConfigPath returns the appropriate config file path based on:
// 1. If ./config.yaml exists in current directory, use it
// 2. If XDG_CONFIG_HOME is set, use $XDG_CONFIG_HOME/microagent/config.yaml
// 3. Otherwise, return DefaultConfigPath()
func DetectConfigPath() (string, error) {
	// Check for local config.yaml
	cwd, err := os.Getwd()
	if err != nil {
		// If we can't get cwd, fall back to default
		return DefaultConfigPath()
	}

	localConfig := filepath.Join(cwd, "config.yaml")
	if _, err := os.Stat(localConfig); err == nil {
		// Local config.yaml exists
		return localConfig, nil
	}

	// Check for XDG_CONFIG_HOME
	if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
		xdgConfigPath := filepath.Join(xdgConfigHome, "microagent", "config.yaml")
		return xdgConfigPath, nil
	}

	// No local config or XDG, use default
	return DefaultConfigPath()
}

// WriteConfig atomically writes cfg as annotated YAML to path with 0600
// permissions. Creates parent directories as needed.
func WriteConfig(path string, cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("configwriter: create directory: %w", err)
	}

	data, err := marshalAnnotated(cfg)
	if err != nil {
		return fmt.Errorf("configwriter: marshal: %w", err)
	}

	// Write to a temp file in the same directory (same filesystem = atomic rename).
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".microagent-config-*.yaml")
	if err != nil {
		return fmt.Errorf("configwriter: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if rename succeeded

	// Set permissions BEFORE writing any secret data (API key).
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("configwriter: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("configwriter: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("configwriter: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("configwriter: rename to final path: %w", err)
	}
	return nil
}

// marshalAnnotated produces annotated YAML using yaml.Node to attach inline
// comments. Falls back to plain yaml.Marshal if node construction fails.
func marshalAnnotated(cfg *config.Config) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	doc.Content = append(doc.Content, root)

	providersNode := buildProvidersNode(cfg)
	modelsNode := buildModelsNode(cfg)
	channelNode := buildChannelNode(cfg)
	storeNode := buildStoreNode(cfg)
	auditNode := buildAuditNode(cfg)

	appendSection(root, "providers", providersNode, "# Provider credentials (provider name → credentials)")
	appendSection(root, "models", modelsNode, "# Active model selection")
	appendSection(root, "channel", channelNode, "# Channel configuration")
	appendSection(root, "store", storeNode, "# Data store configuration")
	appendSection(root, "audit", auditNode, "# Audit configuration")

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		// Fallback: plain marshal
		return yaml.Marshal(cfg)
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

func appendSection(root *yaml.Node, key string, valueNode *yaml.Node, comment string) {
	keyNode := &yaml.Node{
		Kind:        yaml.ScalarNode,
		Tag:         "!!str",
		Value:       key,
		HeadComment: comment,
	}
	root.Content = append(root.Content, keyNode, valueNode)
}

func strNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolNode(value bool) *yaml.Node {
	v := "false"
	if value {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func mappingNode(pairs ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(pairs); i += 2 {
		n.Content = append(n.Content, strNode(pairs[i]), strNode(pairs[i+1]))
	}
	return n
}

// buildProvidersNode builds the v2 "providers:" mapping node.
// Each key is a provider name; value is a credentials mapping.
func buildProvidersNode(cfg *config.Config) *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for name, creds := range cfg.Providers {
		credsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		apiKeyKey := &yaml.Node{
			Kind:        yaml.ScalarNode,
			Tag:         "!!str",
			Value:       "api_key",
			LineComment: "# tip: reference env vars using dollar-brace syntax e.g. MY_API_KEY for better security",
		}
		credsNode.Content = append(credsNode.Content, apiKeyKey, strNode(creds.APIKey))
		if creds.BaseURL != "" {
			credsNode.Content = append(credsNode.Content, strNode("base_url"), strNode(creds.BaseURL))
		}
		root.Content = append(root.Content, strNode(name), credsNode)
	}
	return root
}

// buildModelsNode builds the v2 "models:" mapping node.
func buildModelsNode(cfg *config.Config) *yaml.Node {
	providerKey := &yaml.Node{
		Kind:        yaml.ScalarNode,
		Tag:         "!!str",
		Value:       "provider",
		LineComment: "# options: anthropic, gemini, openrouter, openai, ollama",
	}
	defaultNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	defaultNode.Content = append(defaultNode.Content,
		providerKey, strNode(cfg.Models.Default.Provider),
		strNode("model"), strNode(cfg.Models.Default.Model),
	)
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, strNode("default"), defaultNode)
	return root
}

func buildChannelNode(cfg *config.Config) *yaml.Node {
	n := mappingNode(
		"type", cfg.Channel.Type,
	)
	if cfg.Channel.Token != "" {
		n.Content = append(n.Content, strNode("token"), strNode(cfg.Channel.Token))
	}

	// allowed_users sequence — always written so the field is visible in the YAML.
	seqNode := &yaml.Node{
		Kind:        yaml.SequenceNode,
		Tag:         "!!seq",
		LineComment: "# list of allowed user IDs (int64)",
	}
	for _, id := range cfg.Channel.AllowedUsers {
		seqNode.Content = append(seqNode.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!int",
			Value: strconv.FormatInt(id, 10),
		})
	}
	n.Content = append(n.Content, strNode("allowed_users"), seqNode)

	return n
}

func buildStoreNode(cfg *config.Config) *yaml.Node {
	storePath := cfg.Store.Path
	if storePath == "" {
		storePath = "~/.microagent/data"
	}
	return mappingNode(
		"type", cfg.Store.Type,
		"path", storePath,
	)
}

func buildAuditNode(cfg *config.Config) *yaml.Node {
	auditPath := cfg.Audit.Path
	if auditPath == "" {
		auditPath = "~/.microagent/audit"
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	n.Content = append(n.Content,
		strNode("enabled"), boolNode(cfg.Audit.Enabled),
		strNode("type"), strNode(cfg.Audit.Type),
		strNode("path"), strNode(auditPath),
	)
	return n
}
