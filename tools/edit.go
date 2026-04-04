package tools

import (
	"fmt"
	"os"
	"strings"
)

const editMaxResultChars = 10_000

// EditTool performs an exact-string replacement inside a file.
// Mirrors src/tools/FileEditTool in the TypeScript source.
type EditTool struct{}

func NewEditTool() *EditTool { return &EditTool{} }

func (t *EditTool) Name() string            { return "Edit" }
func (t *EditTool) IsEnabled() bool         { return true }
func (t *EditTool) MaxResultSizeChars() int { return editMaxResultChars }

func (t *EditTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *EditTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *EditTool) Description() string {
	return "Replace an exact string in a file. The old_string must match uniquely unless replace_all is true."
}

func (t *EditTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to edit.",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "Exact string to search for. Must appear exactly once unless replace_all is true.",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "Replacement string.",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, replace every occurrence of old_string. Default false.",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

func (t *EditTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

func (t *EditTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	oldStr, _ := input["old_string"].(string)
	newStr, _ := input["new_string"].(string)
	replaceAll, _ := input["replace_all"].(bool)

	if filePath == "" {
		return ToolResult{IsError: true, Data: "file_path is required"}, nil
	}
	if oldStr == "" {
		return ToolResult{IsError: true, Data: "old_string must not be empty"}, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("read error: %v", err)}, nil
	}
	original := string(data)

	count := strings.Count(original, oldStr)
	if count == 0 {
		return ToolResult{IsError: true, Data: fmt.Sprintf("old_string not found in %s", filePath)}, nil
	}
	if count > 1 && !replaceAll {
		return ToolResult{
			IsError: true,
			Data: fmt.Sprintf(
				"old_string matches %d times in %s — use replace_all: true or provide more context to make it unique",
				count, filePath,
			),
		}, nil
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(original, oldStr, newStr)
	} else {
		updated = strings.Replace(original, oldStr, newStr, 1)
	}

	if err := os.WriteFile(filePath, []byte(updated), 0o644); err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("write error: %v", err)}, nil
	}

	if ctx.ReadFileState != nil {
		ctx.ReadFileState.Set(filePath, fnvHash(updated))
	}

	noun := "occurrence"
	if replaceAll && count > 1 {
		noun = fmt.Sprintf("%d occurrences", count)
	}
	return ToolResult{
		Data: fmt.Sprintf("Replaced %s in %s", noun, filePath),
	}, nil
}
