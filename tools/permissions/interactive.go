package permissions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/claude-code/go-claude-go/tools"
)

// PromptForPermission presents a CLI prompt asking the user whether to allow a
// tool invocation.  It returns:
//
//   - PermAllow  — user chose y or a (always)
//   - PermBlock  — user chose n or N (never) — or stdin is not a terminal
//
// When the user chooses "always" (a), a new AlwaysAllowRule for the tool name
// is added via ctx.SetAppState so future invocations are auto-approved.
//
// When the user chooses "never" (N), a new AlwaysDenyRule is added.
func PromptForPermission(
	toolName string,
	input map[string]interface{},
	ctx tools.ToolContext,
) (tools.PermissionResult, error) {
	// Non-interactive stdin → treat PermAsk as deny.
	if !isTerminal(os.Stdin) {
		return tools.PermissionResult{
			Behavior: tools.PermBlock,
			Reason:   "non-interactive session: tool requires explicit approval",
		}, nil
	}

	printPrompt(toolName, input)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprint(os.Stderr, "Choice [y/n/a/N] (y=yes, n=no, a=always allow, N=never allow): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF or read error → deny
			return tools.PermissionResult{
				Behavior: tools.PermBlock,
				Reason:   "prompt read error",
			}, nil
		}
		rawChoice := strings.TrimSpace(line)
		choice := strings.ToLower(rawChoice)

		// Handle case-sensitive "N" (permanent deny) before lowercased matching.
		if rawChoice == "N" || choice == "never" {
			addDenyRule(toolName, ctx)
			recordDenial(toolName, ctx)
			return tools.PermissionResult{
				Behavior: tools.PermBlock,
				Reason:   "permanently denied by user",
			}, nil
		}

		switch choice {
		case "y", "yes":
			return tools.PermissionResult{
				Behavior:     tools.PermAllow,
				UpdatedInput: input,
			}, nil

		case "n", "no":
			recordDenial(toolName, ctx)
			return tools.PermissionResult{
				Behavior: tools.PermBlock,
				Reason:   "denied by user",
			}, nil

		case "a", "always":
			addAllowRule(toolName, ctx)
			return tools.PermissionResult{
				Behavior:     tools.PermAllow,
				UpdatedInput: input,
			}, nil
		}

		fmt.Fprintln(os.Stderr, "  Please enter y, n, a, or N.")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func printPrompt(toolName string, input map[string]interface{}) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "┌─────────────────────────────────────────────────────┐")
	fmt.Fprintf(os.Stderr,  "│ Claude wants to use: %-31s│\n", toolName)
	fmt.Fprintln(os.Stderr, "├─────────────────────────────────────────────────────┤")

	// Print a concise summary of the input.
	if b, err := json.MarshalIndent(input, "│  ", "  "); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			// Truncate long lines to fit the box
			if len(line) > 51 {
				line = line[:48] + "..."
			}
			fmt.Fprintf(os.Stderr, "│  %-49s│\n", line)
		}
	}
	fmt.Fprintln(os.Stderr, "└─────────────────────────────────────────────────────┘")
}

// addAllowRule appends an AlwaysAllowRule for the given tool name.
func addAllowRule(toolName string, ctx tools.ToolContext) {
	ctx.SetAppState(func(s tools.AppState) tools.AppState {
		s.PermissionContext.AlwaysAllowRules = append(
			s.PermissionContext.AlwaysAllowRules,
			tools.ToolPermissionRule{ToolName: toolName},
		)
		return s
	})
}

// addDenyRule appends an AlwaysDenyRule for the given tool name.
func addDenyRule(toolName string, ctx tools.ToolContext) {
	ctx.SetAppState(func(s tools.AppState) tools.AppState {
		s.PermissionContext.AlwaysDenyRules = append(
			s.PermissionContext.AlwaysDenyRules,
			tools.ToolPermissionRule{ToolName: toolName},
		)
		return s
	})
}

// recordDenial appends a PermissionDenial entry (currently a no-op placeholder;
// engine/submit.go wraps CanUseTool to collect denials at the session level).
func recordDenial(_ string, _ tools.ToolContext) {
	// Denial recording is handled by the engine's CanUseTool wrapper so that
	// the full PermissionDenial (with timestamp) is stored on the QueryEngine.
}

// isTerminal returns true when f is connected to an interactive terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
