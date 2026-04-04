package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// TodoReadTool
// ─────────────────────────────────────────────────────────────────────────────

// TodoReadTool returns the current session todo list.
// Mirrors src/tools/TodoReadTool in the TypeScript source.
type TodoReadTool struct{}

func NewTodoReadTool() *TodoReadTool { return &TodoReadTool{} }

func (t *TodoReadTool) Name() string            { return "TodoRead" }
func (t *TodoReadTool) IsEnabled() bool         { return true }
func (t *TodoReadTool) MaxResultSizeChars() int { return 50_000 }

func (t *TodoReadTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *TodoReadTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *TodoReadTool) Description() string {
	return "Read the current session todo list. Returns all tasks with their status and priority."
}

func (t *TodoReadTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *TodoReadTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *TodoReadTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	if ctx.GetAppState == nil {
		return ToolResult{IsError: true, Data: "session state not available"}, nil
	}
	todos := ctx.GetAppState().Todos
	if len(todos) == 0 {
		return ToolResult{Data: "[]"}, nil
	}
	b, err := json.MarshalIndent(todos, "", "  ")
	if err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("marshal error: %v", err)}, nil
	}
	return ToolResult{Data: string(b)}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// TodoWriteTool
// ─────────────────────────────────────────────────────────────────────────────

// TodoWriteTool replaces the session todo list with a new set of items.
// Mirrors src/tools/TodoWriteTool in the TypeScript source.
type TodoWriteTool struct{}

func NewTodoWriteTool() *TodoWriteTool { return &TodoWriteTool{} }

func (t *TodoWriteTool) Name() string            { return "TodoWrite" }
func (t *TodoWriteTool) IsEnabled() bool         { return true }
func (t *TodoWriteTool) MaxResultSizeChars() int { return 50_000 }

func (t *TodoWriteTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *TodoWriteTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *TodoWriteTool) Description() string {
	return "Write (replace) the session todo list. Provide the complete updated list of todos."
}

func (t *TodoWriteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type":        "array",
				"description": "The complete new todo list. Replaces the existing list.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id": map[string]interface{}{
							"type":        "string",
							"description": "Unique ID. Omit to auto-generate.",
						},
						"content": map[string]interface{}{
							"type":        "string",
							"description": "Task description.",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"pending", "in_progress", "completed"},
							"description": "Task status.",
						},
						"priority": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"high", "medium", "low"},
							"description": "Task priority. Default 'medium'.",
						},
					},
					"required": []string{"content", "status"},
				},
			},
		},
		"required": []string{"todos"},
	}
}

func (t *TodoWriteTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	// Todo mutations are low-risk session-local state; always allow.
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *TodoWriteTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	canUse CanUseToolFn,
	progress chan<- interface{},
) (ToolResult, error) {
	if ctx.SetAppState == nil {
		return ToolResult{IsError: true, Data: "session state not available"}, nil
	}

	rawTodos, ok := input["todos"].([]interface{})
	if !ok {
		return ToolResult{IsError: true, Data: "todos must be an array"}, nil
	}

	newTodos := make([]TodoItem, 0, len(rawTodos))
	for i, rawItem := range rawTodos {
		m, ok := rawItem.(map[string]interface{})
		if !ok {
			return ToolResult{IsError: true, Data: fmt.Sprintf("todos[%d] is not an object", i)}, nil
		}

		content, _ := m["content"].(string)
		if content == "" {
			return ToolResult{IsError: true, Data: fmt.Sprintf("todos[%d]: content is required", i)}, nil
		}

		status := TodoStatus(getString(m, "status"))
		switch status {
		case TodoStatusPending, TodoStatusInProgress, TodoStatusCompleted:
			// valid
		default:
			status = TodoStatusPending
		}

		priority := TodoPriority(getString(m, "priority"))
		switch priority {
		case TodoPriorityHigh, TodoPriorityMedium, TodoPriorityLow:
			// valid
		default:
			priority = TodoPriorityMedium
		}

		id := getString(m, "id")
		if id == "" {
			id = uuid.New().String()
		}

		newTodos = append(newTodos, TodoItem{
			ID:       id,
			Content:  content,
			Status:   status,
			Priority: priority,
		})
	}

	ctx.SetAppState(func(s AppState) AppState {
		s.Todos = newTodos
		return s
	})

	b, _ := json.MarshalIndent(newTodos, "", "  ")
	return ToolResult{Data: string(b)}, nil
}

// getString is a nil-safe map string accessor.
func getString(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}
