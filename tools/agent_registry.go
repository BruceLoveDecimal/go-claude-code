package tools

import (
	"fmt"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// AgentHandle
// ─────────────────────────────────────────────────────────────────────────────

// AgentHandle provides the parent agent with a handle to a running subagent's
// input channel.  The parent can send additional prompts via SendPrompt.
type AgentHandle struct {
	// ID is the unique identifier for this subagent.
	ID string
	// promptCh receives prompts forwarded from SendMessage tool invocations.
	promptCh chan string
}

// SendPrompt forwards a new prompt to the running subagent.  Returns an error
// if the agent has already shut down.
func (h *AgentHandle) SendPrompt(prompt string) error {
	select {
	case h.promptCh <- prompt:
		return nil
	default:
		return fmt.Errorf("agent %q: prompt channel full or agent has exited", h.ID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AgentRegistry
// ─────────────────────────────────────────────────────────────────────────────

// AgentRegistry is a session-scoped concurrent-safe map of running subagents.
// The main engine creates one and passes it via ToolContext so Agent/SendMessage
// tools can register and look up subagents by ID.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*AgentHandle
}

// NewAgentRegistry creates an empty registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{agents: make(map[string]*AgentHandle)}
}

// Register adds an AgentHandle to the registry.  Returns an error if the ID is
// already in use.
func (r *AgentRegistry) Register(handle *AgentHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[handle.ID]; exists {
		return fmt.Errorf("agent %q already registered", handle.ID)
	}
	r.agents[handle.ID] = handle
	return nil
}

// Unregister removes the agent with the given ID (called when the agent exits).
func (r *AgentRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}

// Get looks up an agent by ID.  Returns (nil, false) if not found.
func (r *AgentRegistry) Get(id string) (*AgentHandle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.agents[id]
	return h, ok
}
