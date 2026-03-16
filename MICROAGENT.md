# MICROAGENT — AI Context Document

> **Purpose**: This document is the single source of truth for any AI assistant working on MicroAgent. Read it fully before writing any code, suggesting architecture changes, or making implementation decisions. Every section is intentional — do not skip, summarize, or override unless explicitly told by the developer.

---

## 1. PROJECT IDENTITY

- **Name**: MicroAgent (working name — subject to change before public release)
- **Language**: Go (1.22+)
- **License**: TBD (likely MIT)
- **Author**: Marc Dechand
- **Repository**: Private GitLab (org: JPH Lions, user: MarcDechand) — will move to GitHub for public release
- **Status**: Pre-MVP, architecture phase

**One-line summary**: An ultra-lightweight, single-binary personal AI agent written in Go — designed to be extensible, local-first, and memory-efficient.

---

## 2. PHILOSOPHY & NON-NEGOTIABLES

These principles override any convenience shortcut. If a decision conflicts with these, the principle wins:

1. **Single binary, zero runtime dependencies.** The output is one statically-compiled binary + one YAML config file. No Node.js, no Python, no Docker required to run. `go build` and it works.

2. **Memory budget: <50MB idle, <150MB operating.** Every design choice must respect this. No in-memory caches that grow unbounded. No loading entire conversation histories into RAM. Stream and discard.

3. **Interface-driven extensibility.** The core (agent loop) NEVER imports a concrete implementation. It only knows interfaces. Adding a new channel, provider, tool, or store means: implement interface → register → configure. Zero changes to the core.

4. **Local-first, privacy by default.** All data (conversations, memory, config) lives on the user's filesystem. Nothing leaves the machine unless the user explicitly configures an external provider or channel. No telemetry, no analytics, no phoning home.

5. **Explicit over implicit.** No magic. No auto-discovery of plugins. No reflection-based registration. Everything is wired at compile time or through explicit config. A developer reading `main.go` should understand the entire startup sequence.

6. **Security by restriction.** Shell execution uses a whitelist model by default. Tools have configurable timeouts. The agent loop has iteration limits. Fail closed, not open.

7. **Go idioms, not framework patterns.** Use stdlib where possible. Accept interfaces, return structs. Errors are values — handle them, don't panic. Context propagation everywhere. No `init()` side effects beyond registration.

---

## 3. ARCHITECTURE OVERVIEW

```
┌─────────────────────────────────────────────────────────────┐
│                        main.go                              │
│  Load config → Build registry → Wire components → Run       │
└─────────────┬───────────────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────┐
│                     Agent Loop                              │
│  inbox chan ← Channel.Start()                               │
│                                                             │
│  for msg := range inbox {                                   │
│      context  = buildContext(msg, memory, tools)             │
│      response = provider.Chat(context)                      │
│      while response.hasToolCalls() {                        │
│          results = executeTools(response.toolCalls)          │
│          response = provider.Chat(context + results)         │
│      }                                                      │
│      channel.Send(response.text)                            │
│      store.Save(conversation)                               │
│  }                                                          │
└──────┬──────────┬──────────────┬────────────┬───────────────┘
       │          │              │            │
       ▼          ▼              ▼            ▼
   Channel    Provider         Tool         Store
  (interface) (interface)   (interface)   (interface)
       │          │              │            │
       ▼          ▼              ▼            ▼
   ┌──────┐  ┌──────────┐  ┌────────┐   ┌──────────┐
   │ CLI  │  │Anthropic │  │ Shell  │   │FileStore │
   │Telegr│  │ OpenAI   │  │FileOps │   │ SQLite   │
   │Discrd│  │ Ollama   │  │  HTTP  │   │          │
   └──────┘  └──────────┘  │Browser │   └──────────┘
                           │  MCP   │
                           └────────┘
```

### Component Responsibilities

| Component | Responsibility | Knows about |
|-----------|---------------|-------------|
| `main.go` | Wire everything, start agent | Config, Registry, all concrete types |
| `Agent Loop` | Orchestrate message → LLM → tools → response cycle | Interfaces only: Channel, Provider, Tool, Store |
| `Channel` | Receive user messages, send agent responses | Nothing — it's a dumb pipe |
| `Provider` | Translate ChatRequest into LLM API calls, parse responses | Its own API format only |
| `Tool` | Execute a specific capability, return results | Its own domain only |
| `Store` | Persist and retrieve conversations + memory | Its own storage backend |
| `Registry` | Hold references to all available components by name | Interfaces + factory functions |
| `Config` | Parse YAML, resolve env vars, validate | Nothing — pure data |

---

## 4. INTERFACES (CONTRACTS)

These are the four core contracts. They are FINAL for the MVP — do not add methods without explicit approval. Smaller interfaces = easier to implement = more extensibility.

### 4.1 Channel

```go
package channel

type IncomingMessage struct {
    ID        string
    ChannelID string            // e.g., "cli", "telegram:123456"
    SenderID  string
    Text      string
    Metadata  map[string]string // channel-specific data
    Timestamp time.Time
}

type OutgoingMessage struct {
    ChannelID string
    RecipientID string
    Text      string
    Metadata  map[string]string
}

type Channel interface {
    // Name returns the channel identifier (e.g., "cli", "telegram")
    Name() string

    // Start begins listening for messages and pushes them into inbox.
    // MUST be non-blocking — launch goroutines internally.
    // The channel OWNS its goroutines and must stop them when ctx is cancelled.
    Start(ctx context.Context, inbox chan<- IncomingMessage) error

    // Send delivers a message back through the channel.
    Send(ctx context.Context, msg OutgoingMessage) error

    // Stop gracefully shuts down the channel.
    Stop() error
}
```

**Rules for Channel implementations:**
- `Start()` MUST return immediately. All blocking work goes in goroutines.
- Goroutines MUST respect `ctx.Done()` for clean shutdown.
- Channels MUST NOT import or reference the agent loop, provider, or any other component.
- If the channel loses connection (e.g., Telegram network issue), it should retry internally with backoff, not crash the process.

### 4.2 Provider

```go
package provider

type ChatMessage struct {
    Role       string          `json:"role"`        // "user", "assistant", "tool"
    Content    string          `json:"content"`
    ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
    ToolCallID string          `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
    ID       string          `json:"id"`
    Name     string          `json:"name"`
    Input    json.RawMessage `json:"input"`
}

type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"input_schema"`
}

type ChatRequest struct {
    SystemPrompt string
    Messages     []ChatMessage
    Tools        []ToolDefinition
    MaxTokens    int
    Temperature  float64
}

type ChatResponse struct {
    Content   string      // text content (may be empty if only tool calls)
    ToolCalls []ToolCall  // tool calls to execute (may be empty if only text)
    Usage     UsageStats
    StopReason string    // "end_turn", "tool_use", "max_tokens"
}

type UsageStats struct {
    InputTokens  int
    OutputTokens int
}

type Provider interface {
    Name() string
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    SupportsTools() bool
}
```

**Rules for Provider implementations:**
- MUST handle HTTP errors, rate limits (429), and retries internally with exponential backoff.
- MUST respect `ctx` for cancellation and timeouts.
- MUST map between the internal ChatMessage format and the provider's API format (e.g., Anthropic's `messages` format vs. OpenAI's `chat/completions` format). The agent loop NEVER deals with provider-specific JSON.
- Token counting for context window management is the provider's responsibility.
- DO NOT stream by default in MVP. Add streaming as opt-in in Phase 3.

### 4.3 Tool

```go
package tool

type ToolResult struct {
    Content string // text result returned to the LLM
    IsError bool   // if true, content is an error message
}

type Tool interface {
    // Name is the function name the LLM will call (e.g., "shell_exec", "read_file")
    Name() string

    // Description is what the LLM sees to decide when to use this tool
    Description() string

    // Schema returns the JSON Schema for the tool's input parameters
    Schema() json.RawMessage

    // Execute runs the tool with the given parameters
    Execute(ctx context.Context, params json.RawMessage) (ToolResult, error)
}
```

**Rules for Tool implementations:**
- `Name()` MUST be snake_case and globally unique.
- `Description()` should be concise but sufficient for the LLM to understand when and how to use the tool. This string goes directly into the system prompt context.
- `Schema()` MUST return valid JSON Schema. The LLM uses this to generate correct parameters.
- `Execute()` MUST respect `ctx` for timeouts. If the context is cancelled, stop work and return immediately.
- Tools MUST NOT have side effects beyond their stated purpose. A `read_file` tool does not write. A `shell_exec` tool does not access the network unless the command does.
- Tool errors should be returned as `ToolResult{IsError: true}`, not as Go errors. Go errors are for infrastructure failures (e.g., "couldn't parse params"). Tool errors are for "command not found" or "file doesn't exist" — the LLM needs to see these to self-correct.

### 4.4 Store

```go
package store

type Conversation struct {
    ID        string            `json:"id"`
    ChannelID string            `json:"channel_id"`
    Messages  []ChatMessage     `json:"messages"`
    Metadata  map[string]string `json:"metadata,omitempty"`
    CreatedAt time.Time         `json:"created_at"`
    UpdatedAt time.Time         `json:"updated_at"`
}

type MemoryEntry struct {
    ID        string    `json:"id"`
    Content   string    `json:"content"`
    Tags      []string  `json:"tags,omitempty"`
    Source    string    `json:"source"` // conversation ID
    CreatedAt time.Time `json:"created_at"`
}

type Store interface {
    // Conversations
    SaveConversation(ctx context.Context, conv Conversation) error
    LoadConversation(ctx context.Context, id string) (*Conversation, error)
    ListConversations(ctx context.Context, channelID string, limit int) ([]Conversation, error)

    // Memory — long-term facts extracted from conversations
    AppendMemory(ctx context.Context, entry MemoryEntry) error
    SearchMemory(ctx context.Context, query string, limit int) ([]MemoryEntry, error)

    // Lifecycle
    Close() error
}
```

**Rules for Store implementations:**
- FileStore (MVP): one JSON file per conversation under `~/.microagent/data/conversations/`, one `memory.json` file with all entries. SearchMemory does case-insensitive substring match.
- Writes MUST be atomic (write to temp file → rename) to prevent corruption on crash.
- MUST NOT load all conversations into memory. Load on demand, by ID.
- SearchMemory MUST return results sorted by recency (newest first).

---

## 5. AGENT LOOP — DETAILED SPECIFICATION

The agent loop is the core of the system. It lives in `internal/agent/loop.go`.

### 5.1 Lifecycle

```
Agent.Run(ctx) → launches goroutine that reads from inbox
    → for each IncomingMessage:
        1. loadOrCreateConversation(msg.ChannelID)
        2. appendUserMessage(msg.Text)
        3. buildContext():
            - system prompt (from config, static)
            - relevant memories (SearchMemory with keywords from msg)
            - conversation history (last N messages, configurable)
            - tool definitions (all enabled tools)
        4. iterationLoop(maxIterations):
            a. provider.Chat(context)
            b. if response has tool_calls:
                - execute each tool (respecting timeouts)
                - append tool results to context
                - continue loop
            c. if response is text only:
                - break loop
        5. channel.Send(response.text)
        6. store.SaveConversation()
```

### 5.2 Context Window Management

The agent MUST NOT send unbounded conversation history to the provider. Strategy:

- **System prompt**: always included (fixed cost, typically <500 tokens)
- **Memory**: top 5 relevant entries, injected as a "## Relevant Memory" section in system prompt
- **Conversation history**: last N messages where N is configurable (default: 20 messages). When the conversation exceeds N, older messages are truncated from the beginning but the first user message is always kept for context.
- **Tool definitions**: all enabled tools, always included

If total estimated tokens exceed 80% of the model's context window, truncate conversation history further (remove oldest messages first).

### 5.3 Error Handling in the Loop

| Error type | Handling |
|-----------|---------|
| Provider returns HTTP 429 (rate limit) | Provider handles retry internally. If exhausted, return error to user: "Rate limited, try again in X seconds" |
| Provider returns HTTP 5xx | Retry up to 3 times with exponential backoff. Then error to user. |
| Provider returns invalid JSON | Log raw response, return error to user: "Received invalid response from AI provider" |
| Tool execution times out | Return ToolResult{IsError: true, Content: "Tool timed out after Xs"} — let the LLM decide next step |
| Tool execution panics | Recover, return ToolResult{IsError: true, Content: "Tool crashed"} |
| Max iterations reached | Stop loop, send partial response if any, append note: "(iteration limit reached)" |
| Total timeout reached | Stop loop immediately, send whatever we have |
| Store fails to save | Log error, DO NOT block the response to the user |

### 5.4 Goroutine Model

```
main goroutine
    │
    ├── Channel.Start() goroutine(s)    ← reads from external source, writes to inbox
    │
    └── Agent.Run() goroutine           ← reads from inbox, processes messages
            │
            ├── provider.Chat()          ← blocking HTTP call (with context timeout)
            └── tool.Execute()           ← blocking call (with context timeout)
```

- Only ONE message is processed at a time (serial processing per agent). This is intentional for MVP — avoids race conditions on conversation state.
- Future multi-agent support will use separate Agent instances with separate inboxes, not concurrent processing within one agent.

---

## 6. PROJECT STRUCTURE

```
microagent/
├── cmd/
│   └── microagent/
│       └── main.go              # Entrypoint: config → registry → wire → run
├── internal/
│   ├── agent/
│   │   ├── agent.go             # Agent struct, Run(), Shutdown()
│   │   ├── loop.go              # Core message processing loop
│   │   └── context.go           # Context builder (system prompt + memory + history + tools)
│   ├── channel/
│   │   ├── channel.go           # Interface + message types
│   │   └── cli.go               # [MVP] CLI implementation (stdin/stdout)
│   ├── provider/
│   │   ├── provider.go          # Interface + request/response types
│   │   └── anthropic.go         # [MVP] Anthropic Claude API client
│   ├── tool/
│   │   ├── tool.go              # Interface + ToolResult type
│   │   ├── registry.go          # Tool registry (map[string]Tool)
│   │   ├── shell.go             # [MVP] Shell command execution
│   │   ├── fileops.go           # [MVP] File read/write/list/delete
│   │   └── httpfetch.go         # [MVP] HTTP GET/POST requests
│   ├── store/
│   │   ├── store.go             # Interface + data types
│   │   └── filestore.go         # [MVP] JSON file-based persistence
│   └── config/
│       └── config.go            # YAML parsing, env var resolution, validation
├── configs/
│   └── default.yaml             # Example configuration
├── go.mod
├── go.sum
└── README.md
```

### Naming Conventions

- **Files**: lowercase, snake_case (e.g., `file_store.go` — but Go convention prefers `filestore.go`)
- **Packages**: single lowercase word matching directory name
- **Interfaces**: noun describing the role (Channel, Provider, Tool, Store) — no `I` prefix
- **Structs**: descriptive name with package context (e.g., `anthropic.Client` not `anthropic.AnthropicClient`)
- **Constructors**: `New` + type name (e.g., `NewCLIChannel()`, `NewAnthropicProvider()`)
- **Errors**: `var ErrNotFound = errors.New("not found")` — package-level sentinel errors

---

## 7. DEPENDENCIES & TOOLING

### 7.1 Go Dependencies (minimal)

| Dependency | Purpose | Why not stdlib |
|-----------|---------|----------------|
| `gopkg.in/yaml.v3` | YAML config parsing | stdlib has no YAML support |
| `github.com/google/uuid` | Generate conversation/memory IDs | stdlib has no UUID |

**That's it for MVP.** Everything else uses stdlib:
- `net/http` for all HTTP clients (Anthropic API, Telegram API, HTTP tool)
- `encoding/json` for all JSON marshaling
- `os`, `os/exec` for shell and file tools
- `context` for cancellation and timeouts
- `log/slog` for structured logging (Go 1.21+)
- `sync` for any synchronization needs
- `crypto/rand` if uuid is not desired

### 7.2 Development Tools

| Tool | Purpose | Command |
|------|---------|---------|
| `go build` | Compile binary | `go build -o microagent ./cmd/microagent` |
| `go test` | Run tests | `go test ./...` |
| `go vet` | Static analysis | `go vet ./...` |
| `golangci-lint` | Linting | `golangci-lint run` |
| `gofumpt` | Formatting (stricter gofmt) | `gofumpt -w .` |
| `goreleaser` | Cross-compilation + release | Used for public releases only |
| `dlv` | Debugger | `dlv debug ./cmd/microagent` |

### 7.3 Build & Cross-Compilation

```bash
# Standard build
go build -ldflags="-s -w" -o microagent ./cmd/microagent

# Cross-compile for Linux ARM (e.g., Raspberry Pi)
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o microagent-arm64 ./cmd/microagent

# Cross-compile for macOS
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o microagent-darwin ./cmd/microagent

# Static binary (no CGO)
CGO_ENABLED=0 go build -ldflags="-s -w" -o microagent ./cmd/microagent
```

`-ldflags="-s -w"` strips debug info, reducing binary size by ~30%.

### 7.4 Testing Strategy

- **Unit tests**: every interface implementation gets tests. Use `testing` stdlib + table-driven tests.
- **Mock interfaces**: define mock implementations in `_test.go` files, no mocking frameworks.
- **Integration test for the agent loop**: use a mock provider that returns scripted responses and verify the full cycle.
- **No external test dependencies** unless strictly necessary. If needed: `github.com/stretchr/testify/assert` for readability, but prefer stdlib.

---

## 8. CONFIGURATION SPEC

```yaml
# ~/.microagent/config.yaml

agent:
  name: "Micro"
  personality: |
    You are a concise, helpful personal assistant.
    You respond in Spanish unless spoken to in another language.
    You prefer short, actionable answers over lengthy explanations.
  max_iterations: 10          # max tool-use cycles per user message
  max_tokens_per_turn: 4096   # max tokens per LLM call
  history_length: 20          # messages to keep in context window
  memory_results: 5           # max memory entries to inject into context

provider:
  type: anthropic              # anthropic | openai | ollama
  model: claude-sonnet-4-5-20250929
  api_key: ${ANTHROPIC_API_KEY}
  base_url: ""                 # override for proxies or custom endpoints
  timeout: 60s
  max_retries: 3

channel:
  type: cli                    # cli | telegram | discord
  # Telegram-specific (when type: telegram)
  # token: ${TELEGRAM_BOT_TOKEN}
  # allowed_users: [123456789]  # whitelist of Telegram user IDs

tools:
  shell:
    enabled: true
    allowed_commands:
      - ls
      - cat
      - grep
      - find
      - wc
      - head
      - tail
      - echo
      - date
      - pwd
    allow_all: false           # DANGER: set true to allow any command
    working_dir: "~"
  file:
    enabled: true
    base_path: "~/workspace"   # root for all file operations (sandboxed)
    max_file_size: "1MB"       # refuse to read/write files larger than this
  http:
    enabled: true
    timeout: 15s
    max_response_size: "512KB"
    blocked_domains: []        # domains to never fetch

store:
  type: file                   # file | sqlite
  path: "~/.microagent/data"

logging:
  level: info                  # debug | info | warn | error
  format: text                 # text | json
  file: ""                     # empty = stderr only

limits:
  tool_timeout: 30s
  total_timeout: 120s
```

### Config Resolution Rules

1. Load YAML file from `--config` flag, or `~/.microagent/config.yaml`, or `./config.yaml` (first found).
2. Resolve `${ENV_VAR}` references in string values.
3. Apply defaults for any missing field.
4. Validate: fail fast with a clear error if required fields are missing (e.g., `provider.api_key`).
5. Expand `~` in all path fields to the user's home directory.

---

## 9. MVP TOOL SPECIFICATIONS

### 9.1 shell_exec

```json
{
  "name": "shell_exec",
  "description": "Execute a shell command on the host system. Only whitelisted commands are allowed unless allow_all is true in config.",
  "input_schema": {
    "type": "object",
    "properties": {
      "command": { "type": "string", "description": "The command to execute (e.g., 'ls -la /tmp')" }
    },
    "required": ["command"]
  }
}
```

**Behavior:**
- Parse command string, extract the base command (first token).
- Check against whitelist. If not allowed, return `ToolResult{IsError: true, Content: "Command 'X' is not in the allowed list"}`.
- Execute with `os/exec.CommandContext()` using the tool timeout from config.
- Capture both stdout and stderr. Return combined output.
- Limit output to 10KB. If exceeded, truncate and append "(output truncated)".

### 9.2 read_file

```json
{
  "name": "read_file",
  "description": "Read the contents of a file. Path is relative to the configured base_path.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string", "description": "Relative file path to read" }
    },
    "required": ["path"]
  }
}
```

### 9.3 write_file

```json
{
  "name": "write_file",
  "description": "Write content to a file. Creates parent directories if needed. Path is relative to the configured base_path.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string", "description": "Relative file path to write" },
      "content": { "type": "string", "description": "Content to write to the file" }
    },
    "required": ["path", "content"]
  }
}
```

### 9.4 list_files

```json
{
  "name": "list_files",
  "description": "List files and directories at the given path. Path is relative to the configured base_path.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string", "description": "Relative directory path to list (default: '.')" }
    }
  }
}
```

### 9.5 http_fetch

```json
{
  "name": "http_fetch",
  "description": "Fetch content from a URL via HTTP GET or POST.",
  "input_schema": {
    "type": "object",
    "properties": {
      "url": { "type": "string", "description": "The URL to fetch" },
      "method": { "type": "string", "enum": ["GET", "POST"], "description": "HTTP method (default: GET)" },
      "body": { "type": "string", "description": "Request body for POST requests" },
      "headers": {
        "type": "object",
        "additionalProperties": { "type": "string" },
        "description": "Optional request headers"
      }
    },
    "required": ["url"]
  }
}
```

**Security for all file tools:**
- ALL paths are resolved relative to `base_path` from config. Path traversal (`../`) beyond base_path MUST be rejected.
- Validate with `filepath.Rel()` after cleaning. If the resolved path escapes the base, return error.

---

## 10. KNOWN PROBLEMS & PITFALLS

These are issues known from OpenClaw, Nanobot, and general agent development. MicroAgent must address or consciously defer each one:

### 10.1 Addressed in MVP

| Problem | How MicroAgent handles it |
|---------|--------------------------|
| **Unbounded token costs** | `max_iterations` + `max_tokens_per_turn` limits per message |
| **Shell injection** | Whitelist model. Commands parsed, base command checked before execution |
| **Path traversal in file tools** | All paths sandboxed under `base_path`, traversal beyond it rejected |
| **Infinite agent loop** | Hard cap on iterations + total timeout |
| **Tool crash takes down agent** | Recover from panics in tool execution, return error result to LLM |
| **Large file reads blow up memory** | `max_file_size` config, refuse to read files above threshold |
| **Large HTTP responses blow up memory** | `max_response_size` config, truncate |
| **Secrets in config files** | `${ENV_VAR}` resolution, secrets never stored in YAML directly |

### 10.2 Deferred (Post-MVP)

| Problem | Phase | Notes |
|---------|-------|-------|
| **Prompt injection via tool results** | 3 | Malicious content in fetched URLs or files could hijack the agent. Mitigation: output sanitization, content length limits, optional sandboxing |
| **Multi-user isolation** | 4 | MVP is single-user. Multi-user needs separate agent instances with isolated stores |
| **Concurrent message processing** | 4 | MVP processes one message at a time. Queuing needed for high-throughput channels |
| **Context window overflow** | 3 | MVP uses simple truncation. Better: summarization of old messages |
| **Memory relevance decay** | 5 | MVP uses keyword search. Better: embeddings + vector similarity |
| **Tool output hallucination** | 3 | LLM may misinterpret tool results. Mitigation: structured output validation |

---

## 11. CODING STANDARDS

### 11.1 Go Style

- Follow [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments).
- Use `gofumpt` for formatting (stricter than `gofmt`).
- Use `golangci-lint` with default configuration.
- Every exported function and type MUST have a doc comment.
- Unexported helpers do not need doc comments but SHOULD have them if the logic is non-obvious.
- Error messages are lowercase, no punctuation: `fmt.Errorf("failed to read config: %w", err)`.
- Use `%w` for error wrapping to enable `errors.Is()` / `errors.As()` checks.

### 11.2 Logging

Use `log/slog` (stdlib, Go 1.21+):

```go
slog.Info("processing message",
    "channel", msg.ChannelID,
    "sender", msg.SenderID,
    "length", len(msg.Text),
)

slog.Error("provider call failed",
    "provider", p.Name(),
    "error", err,
    "attempt", attempt,
)
```

- **DEBUG**: tool inputs/outputs, raw HTTP requests (in development only)
- **INFO**: message received, message sent, tool executed, conversation saved
- **WARN**: tool timeout, rate limit hit, conversation truncated
- **ERROR**: provider failure, store failure, unrecoverable tool error

NEVER log API keys, tokens, or full message content at INFO level or above. Full message content at DEBUG only.

### 11.3 Error Handling Pattern

```go
// DO THIS — wrap errors with context
conv, err := s.store.LoadConversation(ctx, id)
if err != nil {
    return fmt.Errorf("loading conversation %s: %w", id, err)
}

// DO NOT — swallow or ignore errors
conv, _ := s.store.LoadConversation(ctx, id) // BAD

// DO NOT — panic on recoverable errors
if err != nil {
    panic(err) // BAD — only panic for programmer errors
}
```

### 11.4 Context Usage

```go
// Every public method that does I/O takes context as first parameter
func (a *Agent) ProcessMessage(ctx context.Context, msg IncomingMessage) error

// Create child contexts with timeouts for specific operations
toolCtx, cancel := context.WithTimeout(ctx, a.config.Limits.ToolTimeout)
defer cancel()
result, err := tool.Execute(toolCtx, params)
```

---

## 12. ANTHROPIC API INTEGRATION DETAILS

The MVP provider talks to the Anthropic Messages API. Key implementation details:

### 12.1 Endpoint

```
POST https://api.anthropic.com/v1/messages
```

### 12.2 Headers

```
x-api-key: <api_key>
anthropic-version: 2023-06-01
content-type: application/json
```

### 12.3 Request Mapping

Internal `ChatRequest` → Anthropic request body:

```json
{
  "model": "claude-sonnet-4-5-20250929",
  "max_tokens": 4096,
  "system": "<system_prompt with memory and personality>",
  "messages": [
    {"role": "user", "content": "..."},
    {"role": "assistant", "content": [
      {"type": "text", "text": "Let me check that."},
      {"type": "tool_use", "id": "toolu_xxx", "name": "shell_exec", "input": {"command": "ls -la"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_xxx", "content": "total 42\n..."}
    ]}
  ],
  "tools": [
    {
      "name": "shell_exec",
      "description": "...",
      "input_schema": { ... }
    }
  ]
}
```

### 12.4 Response Parsing

The response `content` array may contain mixed blocks:
- `{"type": "text", "text": "..."}` — collect as response text
- `{"type": "tool_use", "id": "...", "name": "...", "input": {...}}` — collect as tool calls

If `stop_reason` is `"tool_use"`, the loop must execute tools and call the API again. If `"end_turn"`, the response is final.

### 12.5 Tool Results Format

When sending tool results back, they go as `role: "user"` messages with `tool_result` content blocks:

```json
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_xxx",
      "content": "command output here",
      "is_error": false
    }
  ]
}
```

---

## 13. PHASE ROADMAP

| Phase | Focus | Key deliverables |
|-------|-------|-----------------|
| **1 — MVP** | Core agent loop + single channel + single provider | Working CLI agent with shell, file, and HTTP tools. Conversation persistence. <50MB idle. |
| **2 — Multi-channel** | Telegram, Discord, webhook input | Channel router, per-channel session isolation, reconnection logic |
| **3 — Multi-provider** | OpenAI, Ollama (local models), provider fallback chain | Streaming support (SSE), provider health checks |
| **4a — MCP Support** | MCP client via `mark3labs/mcp-go`, `MCPToolAdapter` wrapping remote tools into `tool.Tool` interface | Stdio + HTTP transports, config-driven server list, zero changes to agent loop |
| **4b — Markdown Skills** | Load behavioral instructions + dynamic shell-backed tools from `.md` skill files | Prompt injection layer + optional `tool` YAML block per file |
| **4c — Heartbeat/Cron** | `HeartbeatChannel` virtual channel, inbox refactor to share across channels | Proactive scheduled tasks, multi-channel inbox unblocks Phase 2 router |
| **5 — Production** | SQLite store (with AES-256-GCM encrypted secrets table + embedded setup wizard on first launch), embeddings, multi-agent, observability, Docker | Token cost tracking, dashboards, deployment automation, secure key management |

---

## 14. DEFINITION OF DONE — MVP

The MVP is complete when ALL of the following are true:

- [ ] `go build` produces a single binary <15MB
- [ ] Binary starts in <500ms and idles at <50MB RSS
- [ ] Operating memory stays <150MB during multi-tool chains
- [ ] CLI channel works: user types message → agent responds
- [ ] Agent loop correctly handles multi-turn tool use (tool call → result → another tool call → final text)
- [ ] All 5 tools work: shell_exec, read_file, write_file, list_files, http_fetch
- [ ] Shell whitelist enforcement works (blocked commands return error to LLM)
- [ ] File sandboxing works (path traversal rejected)
- [ ] Conversations persist to disk and survive restart
- [ ] Memory extraction and injection works (basic keyword search)
- [ ] Config loads from YAML with env var resolution
- [ ] Iteration limit, tool timeout, and total timeout all enforced
- [ ] `go test ./...` passes with >70% coverage on core packages
- [ ] Adding a new tool requires <50 lines of code and zero changes to the agent loop