package tools

import (
	"fmt"
	"os"
	"strings"
)

const readMaxResultChars = 100_000

// ReadTool reads a file from the filesystem, returning its contents with line
// numbers.  Mirrors src/tools/ReadTool in the TypeScript source.
type ReadTool struct{}

func NewReadTool() *ReadTool { return &ReadTool{} }

func (t *ReadTool) Name() string            { return "Read" }
func (t *ReadTool) IsEnabled() bool         { return true }
func (t *ReadTool) MaxResultSizeChars() int { return readMaxResultChars }
func (t *ReadTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *ReadTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *ReadTool) Description() string {
	return "Read a file from the filesystem and return its contents with line numbers."
}

func (t *ReadTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to read.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Line number to start reading from (1-indexed, default 1).",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of lines to return.",
			},
		},
		"required": []string{"file_path"},
	}
}

func (t *ReadTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *ReadTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	if filePath == "" {
		return ToolResult{IsError: true, Data: "file_path is required"}, nil
	}

	perm, err := canUse(t.Name(), input, ctx)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("permission error: %v", err)}, nil
	}
	if perm.Behavior == PermBlock {
		return ToolResult{IsError: true, Data: fmt.Sprintf("blocked: %s", perm.Reason)}, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("read error: %v", err)}, nil
	}

	lines := strings.Split(string(data), "\n")

	// Apply offset (1-indexed)
	offset := 1
	if v, ok := input["offset"].(float64); ok && v >= 1 {
		offset = int(v)
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	lines = lines[offset-1:]

	// Apply limit
	if v, ok := input["limit"].(float64); ok && v > 0 {
		limit := int(v)
		if limit < len(lines) {
			lines = lines[:limit]
		}
	}

	// Build numbered output (same format as the TypeScript Read tool)
	var sb strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&sb, "%d\t%s\n", offset+i, line)
	}
	result := sb.String()
	if len(result) > readMaxResultChars {
		result = result[:readMaxResultChars] + "\n[output truncated]"
	}
	return ToolResult{Data: result}, nil
}
