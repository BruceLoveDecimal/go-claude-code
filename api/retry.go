package api

import (
	"context"
	"math/rand"
	"time"

	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// RetryConfig
// ─────────────────────────────────────────────────────────────────────────────

// RetryConfig controls the exponential-backoff retry behaviour of API calls.
// It mirrors the retry logic in the TypeScript source (src/services/api/).
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (0 = no retries).
	// Default: 5.
	MaxRetries int

	// InitialDelay is the delay before the first retry.
	// Default: 1 second.
	InitialDelay time.Duration

	// MaxDelay caps the inter-retry wait.
	// Default: 30 seconds.
	MaxDelay time.Duration

	// Multiplier is the backoff growth factor between retries.
	// Default: 2.0.
	Multiplier float64
}

// DefaultRetryConfig returns sensible defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:   5,
		InitialDelay: time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
	}
}

// isRetryable returns true when the API error is transient and the call should
// be retried: 429 (rate-limit), 529 (overloaded), 500/502/503/504 (server
// errors).  Other status codes (401, 403, 400, 413 …) are permanent.
func isRetryable(err error) bool {
	apiErr, ok := err.(*types.APIError)
	if !ok {
		return false
	}
	switch apiErr.Status {
	case 429, 529, 500, 502, 503, 504:
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// StreamMessageWithRetry
// ─────────────────────────────────────────────────────────────────────────────

// StreamMessageWithRetry wraps StreamMessage with exponential-backoff retries.
// On a retryable error it drains both output channels, waits, then retries the
// full streaming call.  Non-retryable errors and context cancellation are
// returned immediately.
//
// The channel semantics are identical to StreamMessage.
func StreamMessageWithRetry(
	ctx context.Context,
	client *Client,
	params StreamParams,
	cfg RetryConfig,
) (<-chan interface{}, <-chan error) {
	outCh := make(chan interface{}, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(outCh)
		defer close(errCh)

		delay := cfg.InitialDelay
		for attempt := 0; ; attempt++ {
			msgCh, streamErrCh := StreamMessage(ctx, client, params)

			// Relay events and collect the final error.
			var streamErr error
			for event := range msgCh {
				outCh <- event
			}
			streamErr = <-streamErrCh

			if streamErr == nil {
				return // success
			}

			// Check for context cancellation first.
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			if !isRetryable(streamErr) || attempt >= cfg.MaxRetries {
				errCh <- streamErr
				return
			}

			// Jitter: sleep for delay * [0.5, 1.5)
			jitter := time.Duration(float64(delay) * (0.5 + rand.Float64()))
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case <-time.After(jitter):
			}

			// Grow delay, capped at MaxDelay.
			delay = time.Duration(float64(delay) * cfg.Multiplier)
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
		}
	}()

	return outCh, errCh
}
