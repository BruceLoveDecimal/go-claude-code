package tools

import (
	"fmt"
	"sort"
)

// Registry maps tool names to their Tool implementations.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.  Panics if a tool with the same name
// was already registered.
func (r *Registry) Register(t Tool) {
	name := t.Name()
	if _, exists := r.tools[name]; exists {
		panic(fmt.Sprintf("tool %q already registered", name))
	}
	r.tools[name] = t
}

// RegisterIfAbsent adds a tool to the registry.  Unlike Register, it silently
// ignores duplicate names instead of panicking.
func (r *Registry) RegisterIfAbsent(t Tool) {
	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.tools[name] = t
	}
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns every registered tool in insertion-stable order (map iteration
// is random; callers that need stable order should sort).
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

// Enabled returns the subset of tools where IsEnabled() == true.
func (r *Registry) Enabled() []Tool {
	var out []Tool
	for _, t := range r.tools {
		if t.IsEnabled() {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

// DefaultRegistry returns a registry pre-populated with all built-in tools.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	// Read-only exploration tools
	r.Register(NewBashTool())
	r.Register(NewReadTool())
	r.Register(NewGlobTool())
	r.Register(NewGrepTool())
	r.Register(NewLSTool())
	r.Register(NewWebFetchTool())
	// File mutation tools
	r.Register(NewWriteTool())
	r.Register(NewEditTool())
	r.Register(NewMultiEditTool())
	// Session task management
	r.Register(NewTodoReadTool())
	r.Register(NewTodoWriteTool())
	return r
}
