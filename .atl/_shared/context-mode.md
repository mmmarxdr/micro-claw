# Context-Mode Rules — Compact (for sub-agent injection)

These rules protect your context window from flooding. They are MANDATORY.

## Think in Code

When you need to analyze, count, filter, compare, search, parse, transform, or process data: **write code** that does the work via `ctx_execute(language, code)` and `console.log()` only the answer. Do NOT read raw data into context to process mentally. Your role is PROGRAM the analysis, not COMPUTE it. Write robust, pure JavaScript — no npm dependencies, only Node.js built-ins.

## BLOCKED — do NOT attempt

- `curl` / `wget` → use `ctx_fetch_and_index(url, source)` instead
- `fetch('http...)` / `requests.get(` in shell → use `ctx_execute(language, code)` instead
- Direct web fetching tools → use `ctx_fetch_and_index` + `ctx_search`

## Tool Hierarchy

1. **GATHER**: `ctx_batch_execute(commands, queries)` — Primary tool. Runs all commands, auto-indexes output, returns search results. ONE call replaces 30+ individual calls.
2. **FOLLOW-UP**: `ctx_search(queries)` — Query indexed content. Pass ALL questions as array in ONE call.
3. **PROCESSING**: `ctx_execute(language, code)` | `ctx_execute_file(path, language, code)` — Sandbox execution. Only stdout enters context.
4. **WEB**: `ctx_fetch_and_index(url, source)` then `ctx_search(queries)` — Fetch, chunk, index, query.
5. **INDEX**: `ctx_index(content, source)` — Store content in knowledge base for later search.

## REDIRECTED tools

- **Shell (>20 lines output)** → use `ctx_execute(language: "shell", code: "...")` instead
- **File reading for analysis** → use `ctx_execute_file(path, language, code)` instead
- **grep / search with large results** → use `ctx_execute(language: "shell", code: "grep ...")` instead

## Output Constraints

- Keep responses under 500 words
- Write artifacts (code, configs) to FILES — never return them as inline text
- When indexing, use descriptive source labels
