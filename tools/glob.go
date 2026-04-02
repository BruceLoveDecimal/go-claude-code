package tools

import (
	"io/fs"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	baseDir := ctx.WorkingDir
	if v, ok := input["path"].(string); ok && v != "" {
		baseDir = v
	}

	searchRoot := baseDir
	matchPattern := filepath.ToSlash(pattern)
	if filepath.IsAbs(pattern) {
		searchRoot = string(filepath.Separator)
	}
	if searchRoot == "" {
		searchRoot = "."
	}

	type match struct {
		path    string
		modTime int64
	}
	var matches []match
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		target := filepath.ToSlash(path)
		if !filepath.IsAbs(pattern) {
			rel, relErr := filepath.Rel(searchRoot, path)
			if relErr != nil {
				return nil
			}
			target = filepath.ToSlash(rel)
		}
		if !matchGlobPath(matchPattern, target) {
			return nil
		}
		info, infoErr := os.Stat(path)
		if infoErr != nil {
			return nil
		}
		matches = append(matches, match{
			path:    path,
			modTime: info.ModTime().UnixNano(),
		})
		return nil
	})
	if walkErr != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("glob error: %v", walkErr)}, nil
	}

	if len(matches) == 0 {
		return ToolResult{Data: "No files matched."}, nil
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].modTime == matches[j].modTime {
			return matches[i].path < matches[j].path
		}
		return matches[i].modTime > matches[j].modTime
	})

	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		paths = append(paths, m.path)
	}

	result := strings.Join(paths, "\n")
	if len(result) > globMaxResultChars {
		result = result[:globMaxResultChars] + "\n[output truncated]"
	}
	return ToolResult{Data: result}, nil
}
