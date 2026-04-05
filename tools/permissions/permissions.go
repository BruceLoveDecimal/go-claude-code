// Package permissions implements the five-step tool permission decision chain
// that mirrors hasPermissionsToUseTool() in the TypeScript source.
package permissions

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/claude-code/go-claude-go/tools"
)

// HasPermissionsToUseTool runs the five-step decision chain for a tool call:
//  1. bypassPermissions mode → always allow
//  2. AlwaysDenyRules match  → deny
//  3. AlwaysAllowRules match → allow
//  4. tool.IsReadOnly + dontAsk mode → allow
//  5. default                → ask
//
// The returned PermissionResult includes UpdatedInput == input (unchanged) on
// allow paths so callers can pass it straight through to the tool.
func HasPermissionsToUseTool(
	toolName string,
	input map[string]interface{},
	ctx tools.ToolContext,
	permCtx tools.ToolPermissionContext,
) (tools.PermissionResult, error) {
	// ── 1. bypassPermissions → unconditional allow (with bash safety check) ──
	if permCtx.Mode == tools.PermissionModeBypassPermissions {
		res := allow(input)
		if toolName == "Bash" {
			if cmd, _ := input["command"].(string); cmd != "" {
				if c := ClassifyBash(cmd); c.IsDangerous {
					res.Warning = c.Warning
				}
			}
		}
		return res, nil
	}

	path := extractPath(input)

	// ── 2. AlwaysDenyRules ────────────────────────────────────────────────
	for _, rule := range permCtx.AlwaysDenyRules {
		if ruleMatches(rule, toolName, path) {
			reason := "tool denied by rule"
			if rule.ToolName != "" {
				reason = "tool " + rule.ToolName + " is not allowed"
			}
			return tools.PermissionResult{Behavior: tools.PermBlock, Reason: reason}, nil
		}
	}

	// ── 3. AlwaysAllowRules ───────────────────────────────────────────────
	for _, rule := range permCtx.AlwaysAllowRules {
		if ruleMatches(rule, toolName, path) {
			return allow(input), nil
		}
	}

	// ── 4. read-only tool + dontAsk mode ─────────────────────────────────
	if permCtx.Mode == tools.PermissionModeDontAsk || permCtx.Mode == tools.PermissionModeAcceptEdits {
		if isToolReadOnly(toolName, input, ctx) {
			return allow(input), nil
		}
		// acceptEdits also skips prompt for file-editing tools
		if permCtx.Mode == tools.PermissionModeAcceptEdits && isEditTool(toolName) {
			return allow(input), nil
		}
	}

	// ── 5. Default: check tool's own CheckPermissions, then ask ──────────
	if tool, ok := ctx.Registry.Get(toolName); ok {
		res, err := tool.CheckPermissions(input, ctx)
		if err != nil {
			return tools.PermissionResult{Behavior: tools.PermBlock, Reason: err.Error()}, nil
		}
		if res.Behavior == tools.PermAllow {
			if res.UpdatedInput == nil {
				res.UpdatedInput = input
			}
			return res, nil
		}
		if res.Behavior == tools.PermBlock {
			return res, nil
		}
	}

	// If we reach here the tool wants to proceed (or its CheckPermissions
	// returned PermAsk) — escalate to the interactive layer.
	return tools.PermissionResult{Behavior: tools.PermAsk}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule matching
// ─────────────────────────────────────────────────────────────────────────────

// ruleMatches returns true if the rule applies to the given tool+path.
func ruleMatches(rule tools.ToolPermissionRule, toolName, path string) bool {
	// ToolName check: empty means "any tool"
	if rule.ToolName != "" && rule.ToolName != toolName {
		return false
	}
	// PathGlob check: empty means "any path"
	if rule.PathGlob == "" {
		return true
	}
	if path == "" {
		// Rule requires a path but the invocation has none → no match
		return false
	}
	return matchGlobPath(rule.PathGlob, path)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func allow(input map[string]interface{}) tools.PermissionResult {
	return tools.PermissionResult{Behavior: tools.PermAllow, UpdatedInput: input}
}

// extractPath returns the primary file-system path argument from a tool input,
// checking the most common parameter names in order.
func extractPath(input map[string]interface{}) string {
	for _, key := range []string{"file_path", "path", "pattern", "command"} {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// isToolReadOnly asks the registry whether the tool is read-only for this input.
func isToolReadOnly(toolName string, input map[string]interface{}, ctx tools.ToolContext) bool {
	tool, ok := ctx.Registry.Get(toolName)
	if !ok {
		return false
	}
	return tool.IsReadOnly(input)
}

// isEditTool returns true for tools that write/edit files (auto-approved in
// acceptEdits mode).
func isEditTool(name string) bool {
	switch name {
	case "Edit", "Write", "MultiEdit":
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Glob matching (mirrors tools/globmatch.go — duplicated to avoid import cycle)
// ─────────────────────────────────────────────────────────────────────────────

func globToRegexp(pattern string) (*regexp.Regexp, error) {
	p := filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		ch := p[i]
		switch ch {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				if i+2 < len(p) && p[i+2] == '/' {
					b.WriteString(`(?:.*/)?`)
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case '/':
			b.WriteByte('/')
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func matchGlobPath(pattern, relPath string) bool {
	re, err := globToRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(filepath.ToSlash(relPath))
}
