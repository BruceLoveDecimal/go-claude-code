// Package query implements the agentic loop that drives a single conversation
// turn.  It mirrors the TypeScript query() / queryLoop() functions in
// src/query.ts.
package query

import (
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Transition reasons
// ─────────────────────────────────────────────────────────────────────────────

// TransitionReason records why the loop restarted for the current iteration.
type TransitionReason string

const (
	// TransitionNextTurn is the normal reason: tools were executed and the
	// loop continues with their results.
	TransitionNextTurn TransitionReason = "next_turn"

	// TransitionReactiveCompact is used after a reactive context compaction
	// succeeded and we're retrying with a shorter history.
	TransitionReactiveCompact TransitionReason = "reactive_compact_retry"

	// TransitionMaxTokensEscalate is used after escalating the max_tokens
	// override from 8k → 64k.
	TransitionMaxTokensEscalate TransitionReason = "max_output_tokens_escalate"

	// TransitionMaxTokensRecovery is used when we inject a continuation
	// nudge after a max_output_tokens stop.
	TransitionMaxTokensRecovery TransitionReason = "max_output_tokens_recovery"

	// TransitionTokenBudget is used when a per-turn token budget nudge is
	// injected.
	TransitionTokenBudget TransitionReason = "token_budget_continuation"
)

// ─────────────────────────────────────────────────────────────────────────────
// State
// ─────────────────────────────────────────────────────────────────────────────

// State is the mutable snapshot carried between loop iterations.  It is
// destructured at the top of each iteration and updated before `continue`.
//
// The Go version uses explicit assignment rather than spread-to-overwrite
// because Go doesn't have TypeScript's `{...state, field: newVal}` syntax.
type State struct {
	// Messages is the accumulated conversation history.
	Messages []types.Message

	// AutoCompactTracking tracks compaction state and the circuit-breaker
	// counter.
	AutoCompactTracking *compact.AutoCompactTrackingState

	// MaxOutputTokensRecoveryCount counts how many times we have injected a
	// "please continue" nudge after a max_output_tokens stop (limit: 3).
	MaxOutputTokensRecoveryCount int

	// HasAttemptedReactiveCompact is set to true once we have attempted a
	// reactive compact in response to a 413 prompt-too-long error, to avoid
	// infinite retries.
	HasAttemptedReactiveCompact bool

	// MaxOutputTokensOverride is non-nil when the loop has escalated the
	// token budget from the default (8k) to 64k.
	MaxOutputTokensOverride *int

	// TurnCount starts at 1 and increments after each tool-execution round.
	TurnCount int

	// Transition records why the previous iteration continued.  Nil on the
	// first iteration.
	Transition *TransitionReason
}

// initialState constructs the starting State from the given parameters.
func initialState(messages []types.Message) State {
	return State{
		Messages:  messages,
		TurnCount: 1,
	}
}

// withMessages returns a copy of s with Messages replaced.
func (s State) withMessages(messages []types.Message) State {
	s.Messages = messages
	return s
}

// withTransition returns a copy of s with Transition set.
func (s State) withTransition(reason TransitionReason) State {
	s.Transition = &reason
	return s
}

// withAutoCompact returns a copy of s with AutoCompactTracking set.
func (s State) withAutoCompact(tracking *compact.AutoCompactTrackingState) State {
	s.AutoCompactTracking = tracking
	return s
}
