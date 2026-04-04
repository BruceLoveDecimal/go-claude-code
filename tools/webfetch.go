package tools

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	webFetchMaxResultChars = 50_000
	webFetchTimeout        = 30 * time.Second
)

// WebFetchTool fetches the content of a URL and returns it as plain text.
// HTML tags are stripped; a basic text extraction is performed.
// Mirrors src/tools/WebFetchTool in the TypeScript source.
type WebFetchTool struct{}

func NewWebFetchTool() *WebFetchTool { return &WebFetchTool{} }

func (t *WebFetchTool) Name() string            { return "WebFetch" }
func (t *WebFetchTool) IsEnabled() bool         { return true }
func (t *WebFetchTool) MaxResultSizeChars() int { return webFetchMaxResultChars }

func (t *WebFetchTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *WebFetchTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *WebFetchTool) Description() string {
	return "Fetch the content of a URL and return it as plain text (HTML tags stripped)."
}

func (t *WebFetchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch.",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Request timeout in seconds. Default 30.",
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	// Network access requires user confirmation in default mode.
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

func (t *WebFetchTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	url, _ := input["url"].(string)
	if url == "" {
		return ToolResult{IsError: true, Data: "url is required"}, nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ToolResult{IsError: true, Data: "url must start with http:// or https://"}, nil
	}

	timeout := webFetchTimeout
	if v, ok := input["timeout_seconds"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx.Ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("request error: %v", err)}, nil
	}
	req.Header.Set("User-Agent", "go-claude-go/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("fetch error: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ToolResult{
			IsError: true,
			Data:    fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url),
		}, nil
	}

	// Read up to 2× the character limit (bytes ≈ chars for ASCII/UTF-8).
	limitedReader := io.LimitReader(resp.Body, int64(webFetchMaxResultChars)*2)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("read error: %v", err)}, nil
	}

	text := extractText(string(body))
	if len(text) > webFetchMaxResultChars {
		text = text[:webFetchMaxResultChars] + "\n[output truncated]"
	}

	return ToolResult{Data: text}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML → plain text extraction
// ─────────────────────────────────────────────────────────────────────────────

var (
	reScript  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reSpaces  = regexp.MustCompile(`[ \t]+`)
	reNewline = regexp.MustCompile(`\n{3,}`)
)

// extractText performs a best-effort HTML-to-text conversion:
// 1. Remove <script> and <style> blocks.
// 2. Strip all remaining HTML tags.
// 3. Decode common HTML entities.
// 4. Normalise whitespace.
func extractText(html string) string {
	// Remove script/style blocks entirely.
	text := reScript.ReplaceAllString(html, "")
	// Strip tags.
	text = reTag.ReplaceAllString(text, " ")
	// Decode common entities.
	text = decodeHTMLEntities(text)
	// Collapse inline whitespace.
	text = reSpaces.ReplaceAllString(text, " ")
	// Collapse excessive blank lines.
	text = reNewline.ReplaceAllString(strings.TrimSpace(text), "\n\n")
	return text
}

func decodeHTMLEntities(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
		"&mdash;", "—",
		"&ndash;", "–",
		"&hellip;", "…",
	)
	return replacer.Replace(s)
}
