# Built-in Tools

Tools are what turn an LLM into an agent. Daimon ships with six built-in
tools. All file tools sandbox paths under a configurable `base_path`; path
traversal is rejected.

| Tool          | Description                                                | Key config                         |
| ------------- | ---------------------------------------------------------- | ---------------------------------- |
| `shell_exec`  | Runs a whitelisted shell command                           | `tools.shell.allowed_commands`     |
| `read_file`   | Reads a file inside the sandbox                            | `tools.file.base_path`             |
| `write_file`  | Writes a file inside the sandbox                           | `tools.file.base_path`             |
| `list_files`  | Lists a directory inside the sandbox                       | `tools.file.base_path`             |
| `http_fetch`  | HTTP GET/POST raw responses                                | `tools.http.blocked_domains`       |
| `web_fetch`   | Fetches a URL and extracts clean Markdown (~90% fewer tokens) | `tools.web_fetch.jina_enabled`  |

## `web_fetch` — smart web content extraction

`web_fetch` converts web pages to clean Markdown optimized for LLM
consumption. It uses a three-tier extraction strategy and escalates
automatically when needed:

| Tier | Method                                               | When used                                | Tokens              |
| ---- | ---------------------------------------------------- | ---------------------------------------- | ------------------- |
| 1    | Local extraction (go-readability + html-to-markdown) | Default — works for content-rich pages   | ~2K per article     |
| 2    | [Jina Reader API](https://jina.ai/reader/) fallback  | Tier 1 extracts < 200 chars (JS-heavy)   | ~2K per article     |
| 3    | Raw HTTP response                                    | `extract_content: false` is passed       | Full page size      |

The tier used is returned in the tool's metadata (`tier: "1"`, `"2"`, or
`"raw"`).

### Configuration

```yaml
tools:
  web_fetch:
    enabled: true            # default: true
    timeout: 20s             # default: 20s
    max_response_size: 1MB   # default: 1MB
    blocked_domains: []      # domain blocklist
    jina_enabled: true       # enables Tier 2 fallback (default: false)
    jina_api_key: ""         # optional — or MICROAGENT_JINA_API_KEY env var
```

### Examples

- **Tier 1** (static): *"Fetch and summarize https://en.wikipedia.org/wiki/Buenos_Aires"*
- **Tier 2** (JS-heavy, needs Jina): *"Fetch https://www.google.com/search?q=daimon+ai"*
- **Tier 3** (raw HTML): *"Fetch https://httpbin.org/html with extract_content false"*

> Sites like X/Twitter and LinkedIn actively block automated access.
> Neither Tier 1 nor Tier 2 can bypass authentication walls.

## Tool priority

When a name collides, Daimon resolves tools in this order:

```
built-in > skill > MCP
```

A skill cannot shadow a built-in tool; an MCP tool cannot shadow a skill.
