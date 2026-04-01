package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const grepMaxResultChars = 50_000

// GrepTool searches file contents using a regular expression.
// Mirrors src/tools/GrepTool in the TypeScript source.
type GrepTool struct{}

func NewGrepTool() *GrepTool { return &GrepTool{} }

func (t *GrepTool) Name() string            { return "Grep" }
func (t *GrepTool) IsEnabled() bool         { return true }
func (t *GrepTool) MaxResultSizeChars() int { return grepMaxResultChars }
func (t *GrepTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *GrepTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *GrepTool) Description() string {
	return "Search file contents using a regular expression. Returns matching lines with file paths and line numbers."
}

func (t *GrepTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regular expression to search for.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search in. Defaults to working directory.",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to filter files (e.g. \"*.go\", \"**/*.ts\").",
			},
			"-i": map[string]interface{}{
				"type":        "boolean",
				"description": "Case-insensitive search.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *GrepTool) Call(
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

	// Build regex
	flags := ""
	if v, ok := input["-i"].(bool); ok && v {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("invalid regex: %v", err)}, nil
	}

	searchPath := ctx.WorkingDir
	if v, ok := input["path"].(string); ok && v != "" {
		searchPath = v
	}

	globPattern, _ := input["glob"].(string)

	var sb strings.Builder
	totalChars := 0

	walkErr := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		// Apply glob filter
		if globPattern != "" {
			matched, err := filepath.Match(globPattern, d.Name())
			if err != nil || !matched {
				return nil
			}
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if re.MatchString(line) {
				entry := fmt.Sprintf("%s:%d:%s\n", path, lineNum+1, line)
				totalChars += len(entry)
				if totalChars > grepMaxResultChars {
					sb.WriteString("[output truncated]\n")
					return fs.SkipAll
				}
				sb.WriteString(entry)
			}
		}
		return nil
	})
	if walkErr != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("walk error: %v", walkErr)}, nil
	}

	result := sb.String()
	if result == "" {
		result = "No matches found."
	}
	return ToolResult{Data: result}, nil
}
