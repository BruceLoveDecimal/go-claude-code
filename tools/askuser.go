package tools

import (
	"errors"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// AskUserQuestion tool
// ─────────────────────────────────────────────────────────────────────────────

// UserInputFn is a callback that displays question to the user and returns the
// user's response.  SDK callers inject this via QueryEngineConfig so the engine
// is decoupled from any specific I/O mechanism (CLI readline, HTTP, GUI, test
// stub, …).
type UserInputFn func(question string) (string, error)

// askUserTool implements the AskUserQuestion tool.
type askUserTool struct{}

// NewAskUserTool returns a Tool that lets the model ask the user a question.
// The model calls this tool when it needs clarification before proceeding.
//
// The actual I/O is delegated to UserInputFn stored in ToolContext.UserInputFn.
// If UserInputFn is nil the tool returns an error so the model knows it cannot
// request interactive input in the current session.
func NewAskUserTool() Tool {
	return &askUserTool{}
}

func (t *askUserTool) Name() string { return "AskUserQuestion" }

func (t *askUserTool) Description() string {
	return "Ask the user a question and receive their response. " +
		"Use this when you need clarification or additional information from the user " +
		"before proceeding with a task. Do not use this for confirmation of simple actions."
}

func (t *askUserTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user.",
			},
		},
		"required": []string{"question"},
	}
}

func (t *askUserTool) IsConcurrencySafe(_ map[string]interface{}) bool { return false }
func (t *askUserTool) IsReadOnly(_ map[string]interface{}) bool         { return true }
func (t *askUserTool) IsEnabled() bool                                  { return true }
func (t *askUserTool) MaxResultSizeChars() int                          { return 10_000 }

func (t *askUserTool) CheckPermissions(
	_ map[string]interface{},
	_ ToolContext,
) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow}, nil
}

func (t *askUserTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	_ CanUseToolFn,
	_ chan<- interface{},
) (ToolResult, error) {
	question, _ := input["question"].(string)
	if question == "" {
		return ToolResult{Data: "Error: question parameter is required", IsError: true}, nil
	}

	fn := ctx.UserInputFn
	if fn == nil {
		return ToolResult{
			Data: fmt.Sprintf(
				"Cannot ask user: no UserInputFn configured. Question was: %q", question,
			),
			IsError: true,
		}, nil
	}

	answer, err := fn(question)
	if err != nil {
		if errors.Is(err, ErrNoUserInput) {
			return ToolResult{
				Data:    "User did not respond or input is unavailable.",
				IsError: true,
			}, nil
		}
		return ToolResult{Data: fmt.Sprintf("Input error: %v", err), IsError: true}, nil
	}
	return ToolResult{Data: answer}, nil
}

// ErrNoUserInput is returned by UserInputFn implementations when no user input
// is available (e.g. stdin closed, non-interactive session).
var ErrNoUserInput = errors.New("no user input available")
