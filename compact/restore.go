package compact

import (
	"fmt"
	"strings"

	"github.com/claude-code/go-claude-go/types"
)

// PostCompactConfig holds context that should be re-injected after compaction
// so the model does not lose critical session state.
type PostCompactConfig struct {
	// RecentFiles is a list of recently-read file paths to remind the model
	// about (e.g. from ReadFileState).
	RecentFiles []string

	// PlanModeActive indicates the agent is in plan mode (no edits).
	PlanModeActive bool

	// ActiveAgentIDs lists sub-agents that are currently running.
	ActiveAgentIDs []string

	// ExtraContext allows callers to inject arbitrary reminder text.
	ExtraContext string
}

// BuildPostCompactAttachment returns a user message that reminds the model of
// session state that would otherwise be lost after compaction.  Returns nil if
// there is nothing to restore.
func BuildPostCompactAttachment(cfg PostCompactConfig) *types.UserMessage {
	var parts []string

	if len(cfg.RecentFiles) > 0 {
		// Limit to 10 most recent files.
		files := cfg.RecentFiles
		if len(files) > 10 {
			files = files[:10]
		}
		parts = append(parts, fmt.Sprintf("[Recent files in context: %s]", strings.Join(files, ", ")))
	}

	if cfg.PlanModeActive {
		parts = append(parts, "[Plan mode is active — the agent should plan but not execute edits.]")
	}

	if len(cfg.ActiveAgentIDs) > 0 {
		parts = append(parts, fmt.Sprintf("[Active sub-agents: %s]", strings.Join(cfg.ActiveAgentIDs, ", ")))
	}

	if cfg.ExtraContext != "" {
		parts = append(parts, cfg.ExtraContext)
	}

	if len(parts) == 0 {
		return nil
	}

	msg := types.NewUserMessage(strings.Join(parts, "\n"))
	msg.IsMeta = true
	return msg
}
