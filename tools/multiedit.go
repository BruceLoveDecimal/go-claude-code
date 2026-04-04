package tools

import (
	"fmt"
	"os"
	"strings"
)

const multiEditMaxResultChars = 10_000

// MultiEditTool applies a sequence of exact-string replacements to a single
// file in one operation.  Mirrors src/tools/FileEditTool's multi-edit variant
// in the TypeScript source.
type MultiEditTool struct{}

func NewMultiEditTool() *MultiEditTool { return &MultiEditTool{} }

func (t *MultiEditTool) Name() string            { return "MultiEdit" }
func (t *MultiEditTool) IsEnabled() bool         { return true }
func (t *MultiEditTool) MaxResultSizeChars() int { return multiEditMaxResultChars }

func (t *MultiEditTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *MultiEditTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *MultiEditTool) Description() string {
	return "Apply multiple sequential string replacements to a single file in one operation."
}

func (t *MultiEditTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the file to edit.",
			},
			"edits": map[string]interface{}{
				"type":        "array",
				"description": "Ordered list of replacements to apply sequentially.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old_string": map[string]interface{}{
							"type":        "string",
							"description": "Exact string to find. Must be unique in the current state of the file unless replace_all is true.",
						},
						"new_string": map[string]interface{}{
							"type":        "string",
							"description": "Replacement string.",
						},
						"replace_all": map[string]interface{}{
							"type":        "boolean",
							"description": "Replace all occurrences. Default false.",
						},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"file_path", "edits"},
	}
}

func (t *MultiEditTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

func (t *MultiEditTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	if filePath == "" {
		return ToolResult{IsError: true, Data: "file_path is required"}, nil
	}

	editsRaw, ok := input["edits"].([]interface{})
	if !ok || len(editsRaw) == 0 {
		return ToolResult{IsError: true, Data: "edits must be a non-empty array"}, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("read error: %v", err)}, nil
	}
	content := string(data)

	for i, rawEdit := range editsRaw {
		editMap, ok := rawEdit.(map[string]interface{})
		if !ok {
			return ToolResult{IsError: true, Data: fmt.Sprintf("edit[%d] is not an object", i)}, nil
		}
		oldStr, _ := editMap["old_string"].(string)
		newStr, _ := editMap["new_string"].(string)
		replaceAll, _ := editMap["replace_all"].(bool)

		if oldStr == "" {
			return ToolResult{IsError: true, Data: fmt.Sprintf("edit[%d]: old_string must not be empty", i)}, nil
		}

		count := strings.Count(content, oldStr)
		if count == 0 {
			return ToolResult{
				IsError: true,
				Data:    fmt.Sprintf("edit[%d]: old_string not found in current file state", i),
			}, nil
		}
		if count > 1 && !replaceAll {
			return ToolResult{
				IsError: true,
				Data: fmt.Sprintf(
					"edit[%d]: old_string matches %d times — use replace_all: true or add more context",
					i, count,
				),
			}, nil
		}

		if replaceAll {
			content = strings.ReplaceAll(content, oldStr, newStr)
		} else {
			content = strings.Replace(content, oldStr, newStr, 1)
		}
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("write error: %v", err)}, nil
	}

	if ctx.ReadFileState != nil {
		ctx.ReadFileState.Set(filePath, fnvHash(content))
	}

	return ToolResult{
		Data: fmt.Sprintf("Applied %d edit(s) to %s", len(editsRaw), filePath),
	}, nil
}
