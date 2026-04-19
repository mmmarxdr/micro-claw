package main

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"daimon/internal/config"
)

// runConfigCommand dispatches to the appropriate config subcommand handler.
// args is os.Args[2:] (everything after "config").
// cfgPath is the resolved --config value (may be empty).
func runConfigCommand(args []string, cfgPath string) error {
	if len(args) == 0 {
		fmt.Println("Usage: microagent config <show|get|set|validate|path>")
		return nil
	}
	switch args[0] {
	case "show":
		return configShow(args[1:], cfgPath)
	case "get":
		return configGet(args[1:], cfgPath)
	case "set":
		return configSet(args[1:], cfgPath)
	case "validate":
		return configValidate(args[1:], cfgPath)
	case "path":
		return configPath(args[1:], cfgPath)
	case "--help", "-help", "-h":
		fmt.Println("Usage: microagent config <show|get|set|validate|path>")
		return nil
	default:
		return fmt.Errorf("unknown config subcommand: %q\nUsage: microagent config <show|get|set|validate|path>", args[0])
	}
}

// configShow implements `microagent config show`.
func configShow(_ []string, cfgPath string) error {
	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Marshal to YAML, then redact secrets.
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	output := redactSecrets(string(data))
	fmt.Printf("# Config file: %s\n\n", resolved)
	fmt.Print(output)
	return nil
}

// configGet implements `microagent config get <path>`.
func configGet(args []string, cfgPath string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: microagent config get <path>\nExample: microagent config get provider.model")
	}
	dotPath := args[0]

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	val, err := getFieldByPath(cfg, dotPath)
	if err != nil {
		return err
	}

	fmt.Println(val)
	return nil
}

// configSetAliasTable maps legacy v1 dotpaths to v2 dotpath templates.
// The placeholder <active> is resolved at call time to cfg.Models.Default.Provider.
func configSetAliasTable() map[string]string {
	return map[string]string{
		"provider.api_key":  "providers.<active>.api_key",
		"provider.base_url": "providers.<active>.base_url",
		"provider.type":     "models.default.provider",
		"provider.model":    "models.default.model",
	}
}

// resolveAlias performs a prefix match against the alias table and resolves the
// <active> placeholder using cfg.Models.Default.Provider. It returns the
// rewritten path, or an error when <active> is required but empty.
// If path does not match any alias, it is returned unchanged.
func resolveAlias(path string, cfg *config.Config) (string, error) {
	aliases := configSetAliasTable()
	for legacy, template := range aliases {
		if path == legacy || strings.HasPrefix(path, legacy+".") {
			active := cfg.Models.Default.Provider
			if strings.Contains(template, "<active>") && active == "" {
				return "", fmt.Errorf(
					"cannot use alias %q: no active provider set. Set it first with: microagent config set models.default.provider <name>",
					legacy,
				)
			}
			rewritten := strings.ReplaceAll(template, "<active>", active)
			// If path has a suffix beyond the exact alias (unlikely given current
			// table, but guard for future extensions).
			if path != legacy {
				rewritten = rewritten + path[len(legacy):]
			}
			return rewritten, nil
		}
	}
	return path, nil
}

// configSet implements `microagent config set <path> <value>`.
func configSet(args []string, cfgPath string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: microagent config set <path> <value>\nExample: microagent config set provider.model gpt-4")
	}
	dotPath := args[0]
	value := args[1]

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		return err
	}

	// Load the typed Config first so we can resolve alias placeholders.
	cfg, err := config.Load(resolved)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve legacy alias before touching the raw map.
	dotPath, err = resolveAlias(dotPath, cfg)
	if err != nil {
		return err
	}

	// Read raw YAML to preserve env vars and structure.
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var rawMap map[string]interface{}
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if rawMap == nil {
		rawMap = make(map[string]interface{})
	}

	// Set the value in the raw map.
	if err := setFieldInMap(rawMap, dotPath, coerceValue(value)); err != nil {
		return err
	}

	// Marshal back and write atomically.
	data, err := yaml.Marshal(rawMap)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp := resolved + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, resolved); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename config: %w", err)
	}

	fmt.Printf("Set %s = %s\n", dotPath, value)
	return nil
}

// configValidate implements `microagent config validate`.
func configValidate(_ []string, cfgPath string) error {
	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		return err
	}

	_, err = config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config validation FAILED: %v\n", err)
		fmt.Fprintf(os.Stderr, "Config file: %s\n", resolved)
		return err
	}

	fmt.Printf("Config OK (%s)\n", resolved)
	return nil
}

// configPath implements `microagent config path`.
func configPath(_ []string, cfgPath string) error {
	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		return err
	}

	fmt.Println(resolved)
	return nil
}

// redactSecrets replaces API key values in YAML output with "****".
func redactSecrets(yamlStr string) string {
	lines := strings.Split(yamlStr, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, key := range []string{"api_key:", "token:", "encryption_key:"} {
			if strings.HasPrefix(trimmed, key) {
				// Extract the value part.
				parts := strings.SplitN(trimmed, ":", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					if len(val) > 0 && val != `""` && val != "''" {
						indent := line[:len(line)-len(trimmed)]
						lines[i] = indent + parts[0] + ": '****'"
					}
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// getFieldByPath navigates a Config struct using a dot-separated path
// and returns the string representation of the value.
// It handles both struct fields (by yaml tag) and map segments (by key lookup).
func getFieldByPath(cfg *config.Config, dotPath string) (string, error) {
	parts := strings.Split(dotPath, ".")
	v := reflect.ValueOf(cfg).Elem()

	for i, part := range parts {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return "", fmt.Errorf("path %q: nil pointer at %q", dotPath, strings.Join(parts[:i+1], "."))
			}
			v = v.Elem()
		}

		switch v.Kind() { //nolint:exhaustive
		case reflect.Struct:
			field := findFieldByYAMLTag(v, part)
			if !field.IsValid() {
				return "", fmt.Errorf("unknown config path: %q (no field %q)", dotPath, part)
			}
			v = field

		case reflect.Map:
			// Map key must be string-keyed.
			key := reflect.ValueOf(part)
			entry := v.MapIndex(key)
			if !entry.IsValid() {
				// Missing key on read: return zero value representation.
				entry = reflect.New(v.Type().Elem()).Elem()
			}
			// Map values are unaddressable; wrap in interface for further descent.
			// If the value is a struct, we need an addressable copy.
			if entry.Kind() == reflect.Struct {
				// Make an addressable copy so struct field access works.
				tmp := reflect.New(entry.Type()).Elem()
				tmp.Set(entry)
				v = tmp
			} else {
				v = entry
			}

		default:
			return "", fmt.Errorf("path %q: %q is not a struct or map", dotPath, strings.Join(parts[:i], "."))
		}
	}

	// Dereference final pointer.
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "<nil>", nil
		}
		v = v.Elem()
	}

	return formatValue(v), nil
}

// findFieldByYAMLTag finds a struct field by its yaml tag name.
func findFieldByYAMLTag(v reflect.Value, tag string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		yamlTag := t.Field(i).Tag.Get("yaml")
		// Strip options like ",omitempty".
		if idx := strings.Index(yamlTag, ","); idx >= 0 {
			yamlTag = yamlTag[:idx]
		}
		if yamlTag == tag {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// formatValue converts a reflect.Value to its string representation.
func formatValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int64:
		// Check for time.Duration.
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			return v.Interface().(time.Duration).String()
		}
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Slice:
		items := make([]string, v.Len())
		for i := 0; i < v.Len(); i++ {
			items[i] = formatValue(v.Index(i))
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

// providerCredentialsFields is the set of valid yaml field names on ProviderCredentials.
// Used to reject unknown fields when writing providers.<name>.<field>.
var providerCredentialsFields = map[string]bool{
	"api_key":  true,
	"base_url": true,
}

// validateProviderPath checks that a path of the form providers.<name>.<field>
// references a valid ProviderCredentials field. Returns an error for unknown fields.
func validateProviderPath(parts []string) error {
	// parts = ["providers", "<name>", "<field>", ...]
	if len(parts) < 3 {
		return nil // too short to validate — let normal flow handle
	}
	if parts[0] != "providers" {
		return nil // not a providers path
	}
	field := parts[2]
	if !providerCredentialsFields[field] {
		return fmt.Errorf("unknown config path: %q (no field or key %q in providers entry; valid fields: api_key, base_url)", strings.Join(parts, "."), field)
	}
	return nil
}

// setFieldInMap sets a value in a nested map using a dot-separated path.
func setFieldInMap(m map[string]interface{}, dotPath string, value interface{}) error {
	parts := strings.Split(dotPath, ".")

	// Validate provider sub-paths to catch unknown fields early (FR-30).
	if err := validateProviderPath(parts); err != nil {
		return err
	}

	current := m

	for i := 0; i < len(parts)-1; i++ {
		key := parts[i]
		next, ok := current[key]
		if !ok {
			// Create intermediate map.
			newMap := make(map[string]interface{})
			current[key] = newMap
			current = newMap
			continue
		}
		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path %q: %q is not a map", dotPath, strings.Join(parts[:i+1], "."))
		}
		current = nextMap
	}

	current[parts[len(parts)-1]] = value
	return nil
}

// coerceValue converts a string value to the appropriate Go type.
func coerceValue(s string) interface{} {
	// Bool.
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}

	// Duration (e.g. "30s", "5m").
	if d, err := time.ParseDuration(s); err == nil {
		return d.String()
	}

	// Integer.
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}

	return s
}
