// Package hooks provides extensible hook functions invoked by the query loop
// at key lifecycle points.  Mirrors the hooks infrastructure in the TypeScript
// source at src/hooks/.
package hooks

import (
	"context"

	"github.com/claude-code/go-claude-go/types"
)

// StopHookFn is invoked after each API response that contains no tool_use
// blocks (i.e., the model produced a terminal response for the turn).
//
// If ShouldRetry is true the query loop performs one additional API
// round-trip, giving hooks a chance to inject context or modify state before
// the next call.
type StopHookFn func(ctx context.Context, msg *types.AssistantMessage) (ShouldRetry bool, err error)
