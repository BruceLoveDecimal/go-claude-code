package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const lsMaxResultChars = 50_000

// LSTool lists directory contents in a tree format.
// Mirrors src/tools/LSTool in the TypeScript source.
type LSTool struct{}

func NewLSTool() *LSTool { return &LSTool{} }

func (t *LSTool) Name() string            { return "LS" }
func (t *LSTool) IsEnabled() bool         { return true }
func (t *LSTool) MaxResultSizeChars() int { return lsMaxResultChars }

func (t *LSTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *LSTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *LSTool) Description() string {
	return "List directory contents as a tree. Optionally filter with glob patterns."
}

func (t *LSTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to the directory to list.",
			},
			"ignore": map[string]interface{}{
				"type":        "array",
				"description": "Glob patterns to exclude (e.g. [\"*.log\", \".git\"]).",
				"items":       map[string]interface{}{"type": "string"},
			},
		},
		"required": []string{"path"},
	}
}

func (t *LSTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *LSTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	dirPath, _ := input["path"].(string)
	if dirPath == "" {
		return ToolResult{IsError: true, Data: "path is required"}, nil
	}

	var ignorePatterns []string
	if raw, ok := input["ignore"].([]interface{}); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				ignorePatterns = append(ignorePatterns, s)
			}
		}
	}

	var sb strings.Builder
	err := walkDir(&sb, dirPath, dirPath, ignorePatterns, 0)
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("ls error: %v", err)}, nil
	}

	result := sb.String()
	if len(result) > lsMaxResultChars {
		result = result[:lsMaxResultChars] + "\n[output truncated]"
	}
	return ToolResult{Data: result}, nil
}

// walkDir recursively renders dir as an indented tree into sb.
func walkDir(sb *strings.Builder, root, dir string, ignorePatterns []string, depth int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// Sort: directories first, then files, both alphabetically.
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return entries[i].Name() < entries[j].Name()
	})

	prefix := strings.Repeat("  ", depth)
	for _, entry := range entries {
		name := entry.Name()

		// Apply ignore patterns against the name and relative path.
		rel, _ := filepath.Rel(root, filepath.Join(dir, name))
		if shouldIgnore(name, rel, ignorePatterns) {
			continue
		}

		if entry.IsDir() {
			fmt.Fprintf(sb, "%s%s/\n", prefix, name)
			if err := walkDir(sb, root, filepath.Join(dir, name), ignorePatterns, depth+1); err != nil {
				// Skip unreadable subdirectories gracefully.
				fmt.Fprintf(sb, "%s  [unreadable]\n", prefix)
			}
		} else {
			info, _ := entry.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			fmt.Fprintf(sb, "%s%s (%s)\n", prefix, name, humanSize(size))
		}
	}
	return nil
}

// shouldIgnore returns true if name or relPath matches any ignore pattern.
func shouldIgnore(name, relPath string, patterns []string) bool {
	for _, pat := range patterns {
		// Match against bare name (e.g. "*.log") and full relative path.
		if matched, _ := filepath.Match(pat, name); matched {
			return true
		}
		if matched, _ := filepath.Match(pat, relPath); matched {
			return true
		}
		// Also treat pattern as a literal prefix segment (e.g. ".git")
		if name == pat {
			return true
		}
	}
	return false
}

// humanSize formats a byte count as a compact human-readable string.
func humanSize(b int64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%dB", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	}
}
