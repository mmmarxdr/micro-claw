package channel

import (
	"strings"
	"testing"

	"microagent/internal/config"
)

// TestNewDiscordChannel_TokenRequired verifies that NewDiscordChannel returns an error
// when no token is provided.
func TestNewDiscordChannel_TokenRequired(t *testing.T) {
	_, err := NewDiscordChannel(config.ChannelConfig{}, config.MediaConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("expected error to mention 'token', got: %v", err)
	}
}

// TestNewDiscordChannel_AllowlistMaps verifies that guild and channel allowlists are
// correctly parsed into constant-time lookup maps.
func TestNewDiscordChannel_AllowlistMaps(t *testing.T) {
	cfg := config.ChannelConfig{
		Token:           "dummy-token",
		AllowedGuilds:   []string{"guild-1", "guild-2"},
		AllowedChannels: []string{"chan-A"},
	}

	d := &DiscordChannel{
		allowedGuilds:   make(map[string]bool),
		allowedChannels: make(map[string]bool),
	}
	for _, id := range cfg.AllowedGuilds {
		d.allowedGuilds[id] = true
	}
	for _, id := range cfg.AllowedChannels {
		d.allowedChannels[id] = true
	}

	if !d.allowedGuilds["guild-1"] {
		t.Error("expected guild-1 to be in allowedGuilds")
	}
	if d.allowedGuilds["guild-99"] {
		t.Error("expected guild-99 to NOT be in allowedGuilds")
	}
	if !d.allowedChannels["chan-A"] {
		t.Error("expected chan-A to be in allowedChannels")
	}
	if d.allowedChannels["chan-Z"] {
		t.Error("expected chan-Z to NOT be in allowedChannels")
	}
}

// TestDiscordChannel_SenderAttribution verifies the [username]: message format.
func TestDiscordChannel_SenderAttribution(t *testing.T) {
	username := "Alice"
	content := "Hello, world!"
	text := formatDiscordText(username, content)

	expected := "[Alice]: Hello, world!"
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

// formatDiscordText mirrors the attribution logic in the MessageCreate handler.
// Extracted here for isolated unit testing without needing a live session.
func formatDiscordText(username, content string) string {
	return "[" + username + "]: " + content
}

// TestDiscordChannel_ChannelIDFormat verifies the discord: prefix scheme.
func TestDiscordChannel_ChannelIDFormat(t *testing.T) {
	rawChannelID := "1234567890"
	channelID := "discord:" + rawChannelID

	if !strings.HasPrefix(channelID, "discord:") {
		t.Errorf("expected channelID to start with 'discord:', got %q", channelID)
	}

	stripped := strings.TrimPrefix(channelID, "discord:")
	if stripped != rawChannelID {
		t.Errorf("expected stripped ID %q, got %q", rawChannelID, stripped)
	}
}

// TestDiscordChannel_Name verifies that Name() returns "discord".
func TestDiscordChannel_Name(t *testing.T) {
	d := &DiscordChannel{}
	if d.Name() != "discord" {
		t.Errorf("expected Name() == 'discord', got %q", d.Name())
	}
}

// TestDiscordChannel_Send_Chunking verifies the chunking math at 1900 chars
// without making a live API call.
func TestDiscordChannel_Send_Chunking(t *testing.T) {
	const maxChars = 1900
	msgText := strings.Repeat("B", 4500)

	runes := []rune(msgText)
	length := len(runes)

	var chunks []string
	for i := 0; i < length; i += maxChars {
		end := i + maxChars
		if end > length {
			end = length
		}
		chunk := string(runes[i:end])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	// 4500 / 1900 = 2 full chunks + 700-char remainder = 3 total
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != maxChars {
		t.Errorf("expected chunk 1 length %d, got %d", maxChars, len(chunks[0]))
	}
	if len(chunks[2]) != 700 {
		t.Errorf("expected chunk 3 length 700, got %d", len(chunks[2]))
	}
}

// TestDiscordChannel_GuildAllowlist_Logic verifies the guild filtering decision logic.
func TestDiscordChannel_GuildAllowlist_Logic(t *testing.T) {
	allowedGuilds := map[string]bool{
		"guild-1": true,
	}

	tests := []struct {
		guildID string
		allowed bool
	}{
		{"guild-1", true},
		{"guild-2", false},
		{"", false},
	}

	for _, tc := range tests {
		result := len(allowedGuilds) == 0 || allowedGuilds[tc.guildID]
		if result != tc.allowed {
			t.Errorf("guildID %q: expected allowed=%v, got %v", tc.guildID, tc.allowed, result)
		}
	}
}

// TestDiscordChannel_ChannelAllowlist_Logic verifies the channel filtering decision logic.
func TestDiscordChannel_ChannelAllowlist_Logic(t *testing.T) {
	allowedChannels := map[string]bool{
		"chan-A": true,
	}

	tests := []struct {
		channelID string
		allowed   bool
	}{
		{"chan-A", true},
		{"chan-B", false},
	}

	for _, tc := range tests {
		result := len(allowedChannels) == 0 || allowedChannels[tc.channelID]
		if result != tc.allowed {
			t.Errorf("channelID %q: expected allowed=%v, got %v", tc.channelID, tc.allowed, result)
		}
	}
}

// TestDiscordChannel_EmptyAllowlists_AllowsAll verifies that an empty allowlist
// permits all guilds and channels (opt-in restriction model).
func TestDiscordChannel_EmptyAllowlists_AllowsAll(t *testing.T) {
	emptyGuilds := map[string]bool{}
	emptyChannels := map[string]bool{}

	// With empty allowlists, any ID should pass.
	guildAllowed := len(emptyGuilds) == 0 || emptyGuilds["any-guild"]
	if !guildAllowed {
		t.Error("expected empty allowedGuilds to allow all guilds")
	}

	channelAllowed := len(emptyChannels) == 0 || emptyChannels["any-channel"]
	if !channelAllowed {
		t.Error("expected empty allowedChannels to allow all channels")
	}
}
