package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/claude-code/go-claude-go/types"
)

type countingTool struct {
	name  string
	calls *int
}

func (t *countingTool) Name() string                                { return t.name }
func (t *countingTool) Description() string                         { return "" }
func (t *countingTool) InputSchema() map[string]interface{}         { return map[string]interface{}{} }
func (t *countingTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *countingTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *countingTool) IsEnabled() bool                             { return true }
func (t *countingTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}
func (t *countingTool) Call(input map[string]interface{}, ctx ToolContext, canUse CanUseToolFn, progress chan<- interface{}) (ToolResult, error) {
	*t.calls++
	return ToolResult{Data: "ok"}, nil
}
func (t *countingTool) MaxResultSizeChars() int { return 1000 }

func TestRunToolsChecksPermissionOnce(t *testing.T) {
	registry := NewRegistry()
	toolCalls := 0
	registry.Register(&countingTool{name: "Count", calls: &toolCalls})

	permCalls := 0
	canUse := func(toolName string, input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
		permCalls++
		return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
	}

	msgs, _, err := RunTools([]*types.ToolUseBlock{{
		ID:    "toolu_1",
		Name:  "Count",
		Input: []byte(`{}`),
	}}, canUse, ToolContext{Registry: registry})
	if err != nil {
		t.Fatalf("RunTools() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(msgs))
	}
	if permCalls != 1 {
		t.Fatalf("expected 1 permission check, got %d", permCalls)
	}
	if toolCalls != 1 {
		t.Fatalf("expected 1 tool call, got %d", toolCalls)
	}
}

func TestRegistryEnabledReturnsStableSortedOrder(t *testing.T) {
	registry := NewRegistry()
	a := 0
	registry.Register(&countingTool{name: "Read", calls: &a})
	registry.Register(&countingTool{name: "Bash", calls: &a})
	registry.Register(&countingTool{name: "Glob", calls: &a})

	names := []string{}
	for _, tool := range registry.Enabled() {
		names = append(names, tool.Name())
	}

	want := []string{"Bash", "Glob", "Read"}
	if !slices.Equal(names, want) {
		t.Fatalf("Enabled() names = %v, want %v", names, want)
	}
}

func TestGlobSupportsRecursivePatternsAndSortsByModTime(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "a.go")
	newer := filepath.Join(sub, "b.go")
	if err := os.WriteFile(older, []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("package b"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewGlobTool().Call(
		map[string]interface{}{"pattern": "**/*.go"},
		ToolContext{WorkingDir: dir},
		AlwaysAllow,
		nil,
	)
	if err != nil {
		t.Fatalf("Glob.Call() error = %v", err)
	}

	got := result.Data.(string)
	want := newer + "\n" + older
	if got != want {
		t.Fatalf("Glob result = %q, want %q", got, want)
	}
}

func TestGrepGlobMatchesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "src", "nested")
	otherDir := filepath.Join(dir, "test")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "match.go")
	other := filepath.Join(otherDir, "skip.go")
	if err := os.WriteFile(target, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewGrepTool().Call(
		map[string]interface{}{
			"pattern": "needle",
			"path":    dir,
			"glob":    "src/**/*.go",
		},
		ToolContext{WorkingDir: dir},
		AlwaysAllow,
		nil,
	)
	if err != nil {
		t.Fatalf("Grep.Call() error = %v", err)
	}

	got := result.Data.(string)
	if !slices.Contains(strings.Split(strings.TrimSpace(got), "\n"), target+":1:needle") {
		t.Fatalf("Grep result = %q, expected match in %s", got, target)
	}
	if strings.Contains(got, other) {
		t.Fatalf("Grep result unexpectedly included %s: %q", other, got)
	}
}
