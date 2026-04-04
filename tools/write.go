package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

const writeMaxResultChars = 10_000

// WriteTool creates or overwrites a file with the given content.
// Mirrors src/tools/FileWriteTool in the TypeScript source.
type WriteTool struct{}

func NewWriteTool() *WriteTool { return &WriteTool{} }

func (t *WriteTool) Name() string            { return "Write" }
func (t *WriteTool) IsEnabled() bool         { return true }
func (t *WriteTool) MaxResultSizeChars() int { return writeMaxResultChars }

func (t *WriteTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *WriteTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *WriteTool) Description() string {
	return "Write content to a file, creating it or overwriting it entirely."
}

func (t *WriteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to write.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Full content to write to the file.",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (t *WriteTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	// Always require user confirmation — writing files mutates state.
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

func (t *WriteTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	if filePath == "" {
		return ToolResult{IsError: true, Data: "file_path is required"}, nil
	}
	content, _ := input["content"].(string)

	// Create parent directories as needed.
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("mkdir error: %v", err)}, nil
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("write error: %v", err)}, nil
	}

	// Update the session file-state cache so subsequent edits know the file
	// content without re-reading it.
	if ctx.ReadFileState != nil {
		ctx.ReadFileState.Set(filePath, fnvHash(content))
	}

	return ToolResult{
		Data: fmt.Sprintf("Wrote %d bytes to %s", len(content), filePath),
	}, nil
}

// fnvHash returns a short FNV-1a hex digest of s, used to fingerprint file
// content for the ReadFileState cache.
func fnvHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}
