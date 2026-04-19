package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"daimon/internal/config"
)

// WebFetchTool is an intelligent web scraper that extracts readable content
// from a URL and returns it as Markdown. For JS-heavy pages it optionally falls
// back to the Jina Reader API.
type WebFetchTool struct {
	config config.WebFetchConfig
	client *http.Client
}

// NewWebFetchTool constructs a WebFetchTool from the supplied config.
func NewWebFetchTool(cfg config.WebFetchConfig) *WebFetchTool {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	return &WebFetchTool{
		config: cfg,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (t *WebFetchTool) Name() string { return "web_fetch" }
func (t *WebFetchTool) Description() string {
	return "Fetch a URL and extract its readable content as Markdown. " +
		"Use this instead of http_fetch when you want clean, readable text from a web page. " +
		"Accepts any public URL (http/https)."
}

func (t *WebFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The URL to fetch and extract content from"
    },
    "extract_content": {
      "type": "boolean",
      "description": "When true (default), extract readable content as Markdown. When false, return raw response body."
    }
  },
  "required": ["url"]
}`)
}

type webFetchParams struct {
	URL            string `json:"url"`
	ExtractContent *bool  `json:"extract_content"`
}

func (t *WebFetchTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input webFetchParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, fmt.Errorf("parsing params: %w", err)
	}

	// Default extract_content to true.
	extractContent := true
	if input.ExtractContent != nil {
		extractContent = *input.ExtractContent
	}

	// Validate URL.
	parsedURL, err := url.Parse(input.URL)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid URL: %v", err)}, nil
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ToolResult{IsError: true, Content: fmt.Sprintf("unsupported URL scheme %q: only http/https are allowed", parsedURL.Scheme)}, nil
	}

	// Blocked domain check.
	for _, d := range t.config.BlockedDomains {
		if strings.EqualFold(parsedURL.Host, d) || strings.HasSuffix(strings.ToLower(parsedURL.Host), "."+strings.ToLower(d)) {
			return ToolResult{IsError: true, Content: fmt.Sprintf("domain %s is blocked", parsedURL.Host)}, nil
		}
	}

	// Determine max response size.
	maxSizeStr := t.config.MaxResponseSize
	if maxSizeStr == "" {
		maxSizeStr = "1MB"
	}
	maxSize := parseSize(maxSizeStr)

	// --- Tier 1: direct HTTP GET ---
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("creating request: %v", err)}, nil
	}
	req.Header.Set("User-Agent", "daimon/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{IsError: true, Content: "web_fetch request timed out"}, nil
		}
		return ToolResult{IsError: true, Content: fmt.Sprintf("HTTP request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	var bodyReader io.Reader = resp.Body
	if maxSize > 0 {
		bodyReader = io.LimitReader(resp.Body, maxSize+1)
	}
	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("reading response: %v", err)}, nil
	}

	truncated := false
	if maxSize > 0 && int64(len(bodyBytes)) > maxSize {
		bodyBytes = bodyBytes[:maxSize]
		truncated = true
	}

	statusCode := resp.StatusCode

	// raw mode — skip extraction.
	if !extractContent {
		content := string(bodyBytes)
		if truncated {
			content += "\n...(response truncated)"
		}
		return ToolResult{
			Content: fmt.Sprintf("Status: %s\n\n%s", resp.Status, content),
			Meta: map[string]string{
				"url":           input.URL,
				"status_code":   fmt.Sprintf("%d", statusCode),
				"content_bytes": fmt.Sprintf("%d", len(bodyBytes)),
				"tier":          "raw",
				"extracted":     "false",
			},
		}, nil
	}

	// --- Tier 1: readability extraction ---
	tier := "1"
	articleTitle := ""
	var markdownContent string
	extracted := false

	article, readErr := readability.FromReader(strings.NewReader(string(bodyBytes)), parsedURL)
	if readErr == nil && len(strings.TrimSpace(article.TextContent)) > 0 {
		articleTitle = article.Title
		md, mdErr := htmltomarkdown.ConvertString(article.Content)
		if mdErr == nil {
			markdownContent = md
			extracted = true
		}
	}

	// --- Tier 2: Jina Reader fallback ---
	// Use Jina if: extraction failed, or content is very thin (< 200 chars), and Jina is enabled.
	useJina := t.config.JinaEnabled && (len(strings.TrimSpace(markdownContent)) < 200)
	if useJina {
		jinaResult, jinaErr := t.fetchViaJina(ctx, input.URL)
		if jinaErr == nil {
			markdownContent = jinaResult
			tier = "2"
			extracted = true
		}
		// If Jina fails, fall back to whatever Tier 1 produced.
	}

	// Compose final output.
	var sb strings.Builder
	if articleTitle != "" {
		sb.WriteString("# ")
		sb.WriteString(articleTitle)
		sb.WriteString("\n\n")
	}
	if markdownContent != "" {
		sb.WriteString(markdownContent)
	} else {
		// Last resort: return raw body as plain text.
		sb.WriteString(string(bodyBytes))
		extracted = false
	}
	if truncated {
		sb.WriteString("\n\n...(response truncated)")
	}

	return ToolResult{
		Content: sb.String(),
		Meta: map[string]string{
			"url":           input.URL,
			"status_code":   fmt.Sprintf("%d", statusCode),
			"tier":          tier,
			"title":         articleTitle,
			"extracted":     fmt.Sprintf("%v", extracted),
			"content_bytes": fmt.Sprintf("%d", len(bodyBytes)),
		},
	}, nil
}

// fetchViaJina calls the Jina Reader API (r.jina.ai/{url}) and returns the
// markdown content from the response body.
func (t *WebFetchTool) fetchViaJina(ctx context.Context, targetURL string) (string, error) {
	jinaURL := "https://r.jina.ai/" + targetURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating jina request: %w", err)
	}
	req.Header.Set("User-Agent", "daimon/1.0")
	if t.config.JinaAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.JinaAPIKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("jina request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina returned status %d", resp.StatusCode)
	}

	maxSize := parseSize(t.config.MaxResponseSize)
	var bodyReader io.Reader = resp.Body
	if maxSize > 0 {
		bodyReader = io.LimitReader(resp.Body, maxSize+1)
	}
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		return "", fmt.Errorf("reading jina response: %w", err)
	}

	return strings.TrimSpace(string(body)), nil
}
