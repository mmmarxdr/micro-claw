# Async Indexing Specification (async-indexing)

## Purpose

Replace the synchronous `IndexOutput` call in the agent loop with a buffered-channel worker so that FTS5 inserts do not block the tool execution cycle. This is a new capability within the `output-indexing` domain.

## Requirements

### Requirement: Async IndexOutput Worker

The system MUST index tool outputs via a buffered channel worker goroutine. The agent loop MUST enqueue outputs and return immediately without waiting for the INSERT to complete.

#### Scenario: Enqueue returns immediately

- GIVEN the async indexing worker is running
- AND a tool call completes with a non-error result
- WHEN the agent loop calls the enqueue function
- THEN control returns to the agent loop without blocking on the DB write
- AND the FTS5 INSERT happens asynchronously in the worker goroutine

#### Scenario: Worker drains channel on shutdown

- GIVEN the async indexing worker is running with 3 items in the channel
- WHEN the parent context is cancelled (ctx.Done())
- THEN the worker drains all 3 remaining items from the channel before exiting
- AND no indexed items are silently dropped during graceful shutdown

#### Scenario: Backpressure — channel full drops with warning

- GIVEN the buffered channel is at capacity (all slots filled)
- WHEN another output arrives for indexing
- THEN the output is dropped (not indexed)
- AND a `slog.Warn` is emitted with fields `"tool"`, `"reason": "index_queue_full"`
- AND the agent loop is NOT blocked

#### Scenario: Worker logs indexing errors but continues

- GIVEN the async worker dequeues an output
- AND `IndexOutput` returns a non-nil error
- WHEN the worker processes the item
- THEN a `slog.Warn` is emitted with `"error"` and `"tool"` fields
- AND the worker continues processing subsequent items

### Requirement: Worker Lifecycle

The async worker MUST be started when context mode is enabled and the output store is non-nil. It MUST stop when its context is cancelled.

#### Scenario: Worker starts with agent

- GIVEN context_mode is `auto` and an output store is configured
- WHEN the agent is initialized
- THEN the async indexing worker goroutine is started

#### Scenario: Worker stops on context cancellation

- GIVEN the async indexing worker is running
- WHEN the agent context is cancelled
- THEN the worker goroutine exits (no goroutine leak)

#### Scenario: No worker when context mode is off

- GIVEN context_mode is `off`
- WHEN the agent is initialized
- THEN no async indexing worker goroutine is started

### Requirement: Channel Capacity

The indexing channel MUST have a configurable buffer size with a documented default. The default MUST be at least 32 items.

#### Scenario: Default buffer capacity

- GIVEN the worker is initialized without explicit buffer configuration
- WHEN the channel is created
- THEN the channel buffer capacity is ≥ 32
