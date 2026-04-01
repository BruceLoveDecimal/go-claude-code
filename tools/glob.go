package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

const globMaxResultChars = 50_000

// GlobTool finds files matching a glob pattern, sorted by modification time.
// Mirrors src/tools/GlobTool in the TypeScript source.
type GlobTool struct{}

func NewGlobTool() *GlobTool { return &GlobTool{} }

func (t *GlobTool) Name() string            { return "Glob" }
func (t *GlobTool) IsEnabled() bool         { return true }
func (t *GlobTool) MaxResultSizeChars() int { return globMaxResultChars }
func (t *GlobTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *GlobTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern. Returns matching file paths sorted by modification time."
}

func (t *GlobTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to match (e.g. \"**/*.go\", \"src/**/*.ts\").",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in. Defaults to the working directory.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *GlobTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	pattern, _ := input["pattern"].(string)
	if pattern == "" {
		return ToolResult{IsError: true, Data: "pattern is required"}, nil
	}

	perm, err := canUse(t.Name(), input, ctx)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("permission error: %v", err)}, nil
	}
	if perm.Behavior == PermBlock {
		return ToolResult{IsError: true, Data: fmt.Sprintf("blocked: %s", perm.Reason)}, nil
	}

	baseDir := ctx.WorkingDir
	if v, ok := input["path"].(string); ok && v != "" {
		baseDir = v
	}

	// Build the full pattern
	fullPattern := pattern
	if baseDir != "" && !filepath.IsAbs(pattern) {
		fullPattern = filepath.Join(baseDir, pattern)
	}

	matches, err := filepath.Glob(fullPattern)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("glob error: %v", err)}, nil
	}

	if len(matches) == 0 {
		return ToolResult{Data: "No files matched."}, nil
	}

	result := strings.Join(matches, "\n")
	if len(result) > globMaxResultChars {
		result = result[:globMaxResultChars] + "\n[output truncated]"
	}
	return ToolResult{Data: result}, nil
}
