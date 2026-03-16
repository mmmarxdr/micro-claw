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

	"microagent/internal/config"
)

type HTTPFetchTool struct {
	config config.HTTPToolConfig
	client *http.Client
}

func NewHTTPFetchTool(cfg config.HTTPToolConfig) *HTTPFetchTool {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second // default
	}
	return &HTTPFetchTool{
		config: cfg,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (t *HTTPFetchTool) Name() string { return "http_fetch" }
func (t *HTTPFetchTool) Description() string {
	return "Fetch content from a URL via HTTP GET or POST."
}

func (t *HTTPFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
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
}`)
}

type httpParams struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

func (t *HTTPFetchTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input httpParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, fmt.Errorf("parsing params: %w", err)
	}

	if input.Method == "" {
		input.Method = "GET"
	}
	input.Method = strings.ToUpper(input.Method)

	parsedURL, err := url.Parse(input.URL)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid URL: %v", err)}, nil
	}

	for _, d := range t.config.BlockedDomains {
		if strings.EqualFold(parsedURL.Host, d) || strings.HasSuffix(strings.ToLower(parsedURL.Host), "."+strings.ToLower(d)) {
			return ToolResult{IsError: true, Content: fmt.Sprintf("domain %s is blocked", parsedURL.Host)}, nil
		}
	}

	var reqBody io.Reader
	if input.Body != "" {
		reqBody = strings.NewReader(input.Body)
	}

	req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, reqBody)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("creating request failed: %v", err)}, nil
	}

	for k, v := range input.Headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{IsError: true, Content: "HTTP request timed out"}, nil
		}
		return ToolResult{IsError: true, Content: fmt.Sprintf("HTTP request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	if t.config.MaxResponseSize == "" {
		t.config.MaxResponseSize = "512KB"
	}
	maxSize := parseSize(t.config.MaxResponseSize)
	var bodyReader io.Reader = resp.Body
	if maxSize > 0 {
		bodyReader = io.LimitReader(resp.Body, maxSize+1)
	}

	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("reading response failed: %v", err)}, nil
	}

	truncated := ""
	if maxSize > 0 && int64(len(bodyBytes)) > maxSize {
		bodyBytes = bodyBytes[:maxSize]
		truncated = "\n...(response truncated)"
	}

	result := fmt.Sprintf("Status: %s\n\n%s%s", resp.Status, string(bodyBytes), truncated)
	return ToolResult{
		Content: result,
		Meta: map[string]string{
			"url":            input.URL,
			"status_code":    fmt.Sprintf("%d", resp.StatusCode),
			"response_bytes": fmt.Sprintf("%d", len(bodyBytes)),
		},
	}, nil
}
