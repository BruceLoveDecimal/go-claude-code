package types

// Terminal is returned when the agent loop finishes. Reason explains why the
// loop stopped.
type Terminal struct {
	// Reason is one of: "completed", "aborted_streaming", "max_turns",
	// "prompt_too_long", "token_budget_exceeded".
	Reason string
}

// RequestStartEvent is yielded at the beginning of each API round-trip to
// signal that a new streaming request is starting.
type RequestStartEvent struct {
	Type string `json:"type"` // always "stream_request_start"
}

// StreamDeltaEvent carries a text or tool-input delta from the SSE stream.
type StreamDeltaEvent struct {
	Type       string `json:"type"` // "stream_delta"
	DeltaType  string `json:"delta_type"` // "text_delta" | "input_json_delta" | "thinking_delta"
	Index      int    `json:"index"`
	Text       string `json:"text,omitempty"`
	InputJSON  string `json:"input_json,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
}

// SDKMessage is the union of everything that SubmitMessage can yield to callers.
// In Go we use an empty interface so the stream can carry both persistent
// conversation messages and transient SDK events like deltas/request markers.
type SDKMessage interface{}
