# Spec — chat-thinking-ui

Frontend chat treatment for reasoning output. Covers the `reasoning_token` WebSocket message, `ThinkingBlock` collapsible component, auto-collapse on first text token, and dynamic model picker.

## Requirements

**REQ-CTU-1** The WebSocket protocol MUST support a new message type `reasoning_token` with payload `{"type": "reasoning_token", "data": "<reasoning text fragment>"}`. Old clients that do not handle this type MUST be unaffected (existing unknown-type guard in `ChatPage.tsx` already ignores unrecognized frames).

**REQ-CTU-2** `internal/channel/web.go` MUST implement `WriteReasoning(s string)` on `webStreamWriter`, emitting `reasoning_token` frames to the WebSocket connection.

**REQ-CTU-3** The chat frontend MUST maintain a `reasoningBuffer` state separate from the `textBuffer`; `reasoning_token` frames MUST append to `reasoningBuffer`, `token` frames MUST append to `textBuffer`.

**REQ-CTU-4** A `ThinkingBlock` component (`daimon-frontend/src/components/chat/ThinkingBlock.tsx`) MUST render the `reasoningBuffer` content. It MUST be displayed above the assistant message bubble while reasoning is in progress.

**REQ-CTU-5** While `textBuffer` is empty and `reasoningBuffer` is non-empty, `ThinkingBlock` MUST be in the expanded state, streaming content live.

**REQ-CTU-6** When the first `token` (text) frame arrives for the current turn, `ThinkingBlock` MUST auto-collapse and display a summary line: "Thought for Xs" where X is the elapsed time in seconds from first `reasoning_token` to first `token`.

**REQ-CTU-7** The user MUST be able to click (or press Enter) on the collapsed `ThinkingBlock` to toggle it back to the expanded state; clicking the expanded block MUST collapse it again.

**REQ-CTU-8** The `ThinkingBlock` MUST be keyboard-accessible: it MUST be focusable via Tab and MUST respond to Enter/Space to toggle expand/collapse.

**REQ-CTU-9** After a turn completes, the `ThinkingBlock` for that turn MUST remain rendered and toggleable in the chat history (not discarded on turn end).

**REQ-CTU-10** If a turn produces NO `reasoning_token` frames, `ThinkingBlock` MUST NOT be rendered for that turn.

**REQ-CTU-11** The `ThinkingBlock` collapse/expand animation SHOULD use a CSS height transition (not JS-based layout shift) to avoid jank during rapid token streaming.

## Scenarios

#### Scenario CTU-1a: reasoning_token frames populate ThinkingBlock
- **GIVEN** the assistant is streaming reasoning for the current turn
- **WHEN** `reasoning_token` frames arrive on the WebSocket
- **THEN** `ThinkingBlock` MUST be visible above the assistant message area
- **AND** its content MUST update in real-time with each incoming fragment

#### Scenario CTU-1b: Auto-collapse on first text token
- **GIVEN** `ThinkingBlock` is expanded and has been streaming for 4 seconds
- **WHEN** the first `token` frame arrives
- **THEN** `ThinkingBlock` MUST collapse
- **AND** the summary "Thought for 4s" MUST be displayed
- **AND** the main message streaming MUST begin rendering below

#### Scenario CTU-1c: Manual re-expand after auto-collapse
- **GIVEN** `ThinkingBlock` is in the auto-collapsed state ("Thought for 4s")
- **WHEN** the user clicks the collapsed block
- **THEN** it MUST expand to show the full reasoning content
- **AND** the main message text MUST remain visible below

#### Scenario CTU-1d: Enter key toggles block
- **GIVEN** `ThinkingBlock` is focused (via Tab)
- **WHEN** the user presses Enter
- **THEN** the block MUST toggle between expanded and collapsed states

#### Scenario CTU-1e: ThinkingBlock persists in chat history
- **GIVEN** a completed turn that included reasoning
- **WHEN** the user scrolls up to a previous turn
- **THEN** that turn's `ThinkingBlock` MUST be present and toggleable
- **AND** the full reasoning content MUST be available on expand

#### Scenario CTU-1f: No ThinkingBlock for non-reasoning turns
- **GIVEN** a turn where no `reasoning_token` frames were received
- **THEN** NO `ThinkingBlock` MUST be rendered for that turn

#### Scenario CTU-2a: Old frontend — reasoning_token ignored gracefully
- **GIVEN** an old frontend bundle without `reasoning_token` handling
- **AND** the backend sends `reasoning_token` frames during a turn
- **WHEN** the WebSocket message handler processes the frames
- **THEN** the unknown type MUST be silently ignored
- **AND** the assistant text response MUST still render correctly

#### Scenario CTU-3a: WebSocket WriteReasoning emits correct frame
- **GIVEN** the agent loop calls `channelWriter.WriteReasoning("step A")`
- **THEN** the WebSocket connection MUST receive a frame with `{"type": "reasoning_token", "data": "step A"}`
