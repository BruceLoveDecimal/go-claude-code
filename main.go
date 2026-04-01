// main.go demonstrates a complete agent run using the go-claude-go engine.
//
// Usage:
//
//	ANTHROPIC_API_KEY=<key> go run . [prompt]
//
// Default prompt: "List the Go source files in the current directory."
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/claude-code/go-claude-go/engine"
	"github.com/claude-code/go-claude-go/types"
)

func main() {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: ANTHROPIC_API_KEY environment variable is not set")
		os.Exit(1)
	}

	// Determine the prompt from CLI args or use the default.
	prompt := "List the Go source files in the current directory, then read the first one and summarise what it does."
	if len(os.Args) > 1 {
		prompt = strings.Join(os.Args[1:], " ")
	}

	cwd, _ := os.Getwd()

	// Create the QueryEngine with default tools (Bash, Read, Glob, Grep).
	qe := engine.NewQueryEngine(engine.QueryEngineConfig{
		APIKey:  apiKey,
		Model:   "claude-sonnet-4-6",
		CWD:     cwd,
		MaxTurns: 10,
		SystemPrompt: "You are a helpful coding assistant. " +
			"Use the available tools to answer questions about the codebase. " +
			"Be concise and precise.",
	})

	ctx := context.Background()

	fmt.Printf("━━━ go-claude-go demo ━━━\n")
	fmt.Printf("Prompt: %s\n\n", prompt)

	msgCh, errCh := qe.SubmitMessage(ctx, prompt)

	for msg := range msgCh {
		printMessage(msg)
	}

	if err := <-errCh; err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}

	usage := qe.TotalUsage()
	fmt.Printf("\n━━━ Usage ━━━\n")
	fmt.Printf("Input tokens:  %d\n", usage.InputTokens)
	fmt.Printf("Output tokens: %d\n", usage.OutputTokens)
	if usage.CacheReadInputTokens > 0 {
		fmt.Printf("Cache read:    %d\n", usage.CacheReadInputTokens)
	}
}

// printMessage renders a single message to stdout in a human-readable format.
func printMessage(msg types.Message) {
	switch m := msg.(type) {
	case *types.UserMessage:
		if m.IsMeta || m.IsVirtual {
			return
		}
		// Check if it's a tool result (has ToolResultBlock content)
		if hasToolResultBlocks(m) {
			fmt.Printf("\n[tool_result]\n")
			for _, blk := range m.Msg.Content.Blocks {
				if tr, ok := blk.(*types.ToolResultBlock); ok {
					status := "ok"
					if tr.IsError {
						status = "error"
					}
					preview := tr.Content
					if len(preview) > 500 {
						preview = preview[:500] + "…"
					}
					fmt.Printf("  (%s) tool_use_id=%s\n%s\n", status, tr.ToolUseID, indent(preview, "  "))
				}
			}
			return
		}
		// Regular user prompt
		fmt.Printf("\n[user]\n%s\n", m.Msg.Content.Text)

	case *types.AssistantMessage:
		if m.IsAPIErrorMessage {
			fmt.Printf("\n[assistant:error] %v\n", m.APIErr)
			return
		}
		for _, blk := range m.Msg.Content {
			switch b := blk.(type) {
			case *types.TextBlock:
				if b.Text != "" {
					fmt.Printf("\n[assistant]\n%s\n", b.Text)
				}
			case *types.ToolUseBlock:
				fmt.Printf("\n[tool_use] %s(%s)\n", b.Name, string(b.Input))
			case *types.ThinkingBlock:
				if b.Thinking != "" {
					preview := b.Thinking
					if len(preview) > 200 {
						preview = preview[:200] + "…"
					}
					fmt.Printf("\n[thinking] %s\n", preview)
				}
			}
		}

	case *types.SystemMessage:
		if m.Subtype == types.SystemSubtypeCompactBoundary {
			fmt.Printf("\n[compact_boundary] %s\n", m.Content)
		}
		// Skip informational noise (stream_request_start, etc.)
	}
}

func hasToolResultBlocks(m *types.UserMessage) bool {
	for _, blk := range m.Msg.Content.Blocks {
		if _, ok := blk.(*types.ToolResultBlock); ok {
			return true
		}
	}
	return false
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
