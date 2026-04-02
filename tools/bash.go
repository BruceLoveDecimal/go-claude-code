package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const bashMaxResultChars = 30_000

// BashTool executes shell commands and returns stdout + stderr.
// Mirrors src/tools/BashTool in the TypeScript source.
type BashTool struct{}

// NewBashTool constructs a BashTool.
func NewBashTool() *BashTool { return &BashTool{} }

func (t *BashTool) Name() string        { return "Bash" }
func (t *BashTool) IsEnabled() bool     { return true }
func (t *BashTool) MaxResultSizeChars() int { return bashMaxResultChars }

func (t *BashTool) Description() string {
	return "Run a shell command and return its stdout and stderr."
}

func (t *BashTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in milliseconds (default 120000).",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Short description of what this command does.",
			},
		},
		"required": []string{"command"},
	}
}

func (t *BashTool) IsConcurrencySafe(input map[string]interface{}) bool {
	return t.IsReadOnly(input)
}

func (t *BashTool) IsReadOnly(input map[string]interface{}) bool {
	cmd, _ := input["command"].(string)
	return isReadOnlyShellCommand(cmd)
}

func (t *BashTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *BashTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	command, _ := input["command"].(string)
	if command == "" {
		return ToolResult{IsError: true, Data: "command is required"}, nil
	}

	// Build timeout
	timeoutMs := 120_000
	if v, ok := input["timeout"].(float64); ok && v > 0 {
		timeoutMs = int(v)
	}
	deadline := time.Duration(timeoutMs) * time.Millisecond
	execCtx, cancel := context.WithTimeout(ctx.Ctx, deadline)
	defer cancel()

	// Execute
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(execCtx, "bash", "-c", command)
	c.Stdout = &stdout
	c.Stderr = &stderr
	if ctx.WorkingDir != "" {
		c.Dir = ctx.WorkingDir
	}
	runErr := c.Run()

	output := buildBashOutput(stdout.String(), stderr.String(), runErr)
	if len(output) > bashMaxResultChars {
		output = output[:bashMaxResultChars] + "\n[output truncated]"
	}
	isError := runErr != nil
	return ToolResult{Data: output, IsError: isError}, nil
}

// buildBashOutput formats stdout + stderr into a single string the model can
// read.
func buildBashOutput(stdout, stderr string, err error) string {
	var sb strings.Builder
	if stdout != "" {
		sb.WriteString(stdout)
	}
	if stderr != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("STDERR:\n")
		sb.WriteString(stderr)
	}
	if err != nil {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("Exit error: %v", err))
	}
	return sb.String()
}

// isReadOnlyShellCommand is a heuristic that returns true when the command
// looks like it won't mutate any files or processes.
func isReadOnlyShellCommand(cmd string) bool {
	readOnlyPrefixes := []string{
		"ls ", "ls\t", "ls\n", "ls",
		"cat ", "head ", "tail ", "wc ", "echo ", "printf ",
		"find ", "grep ", "rg ", "ag ",
		"git log", "git diff", "git status", "git show",
		"which ", "type ", "file ", "stat ",
		"pwd", "env", "printenv",
	}
	trimmed := strings.TrimSpace(cmd)
	for _, prefix := range readOnlyPrefixes {
		if trimmed == strings.TrimRight(prefix, " \t") ||
			strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
