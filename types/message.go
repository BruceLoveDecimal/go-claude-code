// Package types defines all message and content-block types used throughout
// the go-claude-go agent loop.  The type hierarchy mirrors the TypeScript
// source at src/types/message.ts in the Claude Code repository.
package types

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Message discriminant
// ─────────────────────────────────────────────────────────────────────────────

// MessageType is the top-level discriminant stored in every message's "type"
// field.
type MessageType string

const (
	MessageTypeUser          MessageType = "user"
	MessageTypeAssistant     MessageType = "assistant"
	MessageTypeSystem        MessageType = "system"
	MessageTypeProgress      MessageType = "progress"
	MessageTypeTombstone     MessageType = "tombstone"
	MessageTypeToolUseSummary MessageType = "tool_use_summary"
)

// Message is the union interface for every kind of message the agent loop
// produces or consumes.
type Message interface {
	GetType()      MessageType
	GetUUID()      string
	GetTimestamp() string
}

// ─────────────────────────────────────────────────────────────────────────────
// UserMessage
// ─────────────────────────────────────────────────────────────────────────────

// UserContent holds the role and body of a user turn.
type UserContent struct {
	Role    string        `json:"role"`
	Content RawContent    `json:"content"`
}

// RawContent can be a plain string or an array of content blocks.
// We store both forms and choose during (un)marshalling.
type RawContent struct {
	Text   string
	Blocks []ContentBlock
}

func (r RawContent) MarshalJSON() ([]byte, error) {
	if len(r.Blocks) > 0 {
		return json.Marshal(r.Blocks)
	}
	return json.Marshal(r.Text)
}

func (r *RawContent) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.Text = s
		return nil
	}
	// Try array of raw blocks
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	for _, raw := range raws {
		block, err := UnmarshalContentBlock(raw)
		if err != nil {
			return err
		}
		r.Blocks = append(r.Blocks, block)
	}
	return nil
}

// UserMessage wraps a user turn, optionally carrying tool results or metadata.
type UserMessage struct {
	Type                    MessageType  `json:"type"`
	UUID                    string       `json:"uuid"`
	Timestamp               string       `json:"timestamp"`
	Msg                     UserContent  `json:"message"`
	IsMeta                  bool         `json:"isMeta,omitempty"`
	IsCompactSummary        bool         `json:"isCompactSummary,omitempty"`
	IsVirtual               bool         `json:"isVirtual,omitempty"`
	SourceToolAssistantUUID string       `json:"sourceToolAssistantUUID,omitempty"`
	ToolUseResult           interface{}  `json:"toolUseResult,omitempty"`
}

func (m *UserMessage) GetType()      MessageType { return MessageTypeUser }
func (m *UserMessage) GetUUID()      string       { return m.UUID }
func (m *UserMessage) GetTimestamp() string       { return m.Timestamp }

// NewUserMessage creates a user message with a fresh UUID and current
// timestamp.
func NewUserMessage(content string) *UserMessage {
	return &UserMessage{
		Type:      MessageTypeUser,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Msg: UserContent{
			Role:    "user",
			Content: RawContent{Text: content},
		},
	}
}

// NewToolResultMessage creates a user message that carries one or more
// tool_result content blocks back to the model.
func NewToolResultMessage(blocks []ContentBlock, assistantUUID string) *UserMessage {
	return &UserMessage{
		Type:      MessageTypeUser,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Msg: UserContent{
			Role:    "user",
			Content: RawContent{Blocks: blocks},
		},
		SourceToolAssistantUUID: assistantUUID,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AssistantMessage
// ─────────────────────────────────────────────────────────────────────────────

// APIMessage mirrors the shape of the raw Anthropic API response body.
type APIMessage struct {
	ID           string         `json:"id"`
	Model        string         `json:"model"`
	Role         string         `json:"role"`
	StopReason   string         `json:"stop_reason"`
	Type         string         `json:"type"`
	Usage        Usage          `json:"usage"`
	Content      []ContentBlock `json:"-"` // deserialized separately
	RawContent   []json.RawMessage `json:"content"`
}

// DecodeContent populates Content from RawContent.
func (m *APIMessage) DecodeContent() error {
	m.Content = m.Content[:0]
	for _, raw := range m.RawContent {
		block, err := UnmarshalContentBlock(raw)
		if err != nil {
			return fmt.Errorf("decode content block: %w", err)
		}
		m.Content = append(m.Content, block)
	}
	return nil
}

// APIError holds an HTTP-level error from the Anthropic API.
type APIError struct {
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Body    interface{} `json:"body,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic API error %d: %s", e.Status, e.Message)
}

// AssistantMessage wraps a single model response.
type AssistantMessage struct {
	Type              MessageType `json:"type"`
	UUID              string      `json:"uuid"`
	Timestamp         string      `json:"timestamp"`
	Msg               APIMessage  `json:"message"`
	RequestID         string      `json:"requestId,omitempty"`
	IsAPIErrorMessage bool        `json:"isApiErrorMessage,omitempty"`
	APIErr            *APIError   `json:"apiError,omitempty"`
}

func (m *AssistantMessage) GetType()      MessageType { return MessageTypeAssistant }
func (m *AssistantMessage) GetUUID()      string       { return m.UUID }
func (m *AssistantMessage) GetTimestamp() string       { return m.Timestamp }

// ToolUseBlocks returns all ToolUseBlock items from the message content.
func (m *AssistantMessage) ToolUseBlocks() []*ToolUseBlock {
	var out []*ToolUseBlock
	for _, b := range m.Msg.Content {
		if tb, ok := b.(*ToolUseBlock); ok {
			out = append(out, tb)
		}
	}
	return out
}

// TextContent concatenates all text blocks into a single string.
func (m *AssistantMessage) TextContent() string {
	var s string
	for _, b := range m.Msg.Content {
		if tb, ok := b.(*TextBlock); ok {
			s += tb.Text
		}
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────────────
// SystemMessage
// ─────────────────────────────────────────────────────────────────────────────

// SystemMessageSubtype distinguishes system message kinds.
type SystemMessageSubtype string

const (
	SystemSubtypeInformational      SystemMessageSubtype = "informational"
	SystemSubtypeCompactBoundary    SystemMessageSubtype = "compact_boundary"
	SystemSubtypeMicrocompactBoundary SystemMessageSubtype = "microcompact_boundary"
	SystemSubtypeLocalCommand       SystemMessageSubtype = "local_command"
	SystemSubtypeAPIError           SystemMessageSubtype = "api_error"
)

// SystemMessageLevel controls how the message is displayed.
type SystemMessageLevel string

const (
	SystemLevelInfo    SystemMessageLevel = "info"
	SystemLevelWarning SystemMessageLevel = "warning"
	SystemLevelError   SystemMessageLevel = "error"
)

// SystemMessage carries agent-internal notifications and boundary markers.
type SystemMessage struct {
	Type      MessageType          `json:"type"`
	Subtype   SystemMessageSubtype `json:"subtype"`
	UUID      string               `json:"uuid"`
	Timestamp string               `json:"timestamp"`
	Content   string               `json:"content"`
	Level     SystemMessageLevel   `json:"level"`
	// CompactMetadata is populated for compact_boundary messages.
	CompactMetadata *CompactMetadata `json:"compactMetadata,omitempty"`
}

func (m *SystemMessage) GetType()      MessageType { return MessageTypeSystem }
func (m *SystemMessage) GetUUID()      string       { return m.UUID }
func (m *SystemMessage) GetTimestamp() string       { return m.Timestamp }

// CompactMetadata records token stats around a compaction event.
type CompactMetadata struct {
	PreCompactTokenCount  int `json:"preCompactTokenCount"`
	PostCompactTokenCount int `json:"postCompactTokenCount"`
	MessagesSummarized    int `json:"messagesSummarized"`
}

// NewSystemMessage creates an informational system message.
func NewSystemMessage(subtype SystemMessageSubtype, content string, level SystemMessageLevel) *SystemMessage {
	return &SystemMessage{
		Type:      MessageTypeSystem,
		Subtype:   subtype,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Content:   content,
		Level:     level,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TombstoneMessage
// ─────────────────────────────────────────────────────────────────────────────

// TombstoneMessage marks an AssistantMessage as deleted (used for error
// recovery when we need to remove a message that was already yielded).
type TombstoneMessage struct {
	Type    MessageType       `json:"type"`
	UUID    string            `json:"uuid"`
	Timestamp string          `json:"timestamp"`
	Target  *AssistantMessage `json:"message"`
}

func (m *TombstoneMessage) GetType()      MessageType { return MessageTypeTombstone }
func (m *TombstoneMessage) GetUUID()      string       { return m.UUID }
func (m *TombstoneMessage) GetTimestamp() string       { return m.Timestamp }

// ─────────────────────────────────────────────────────────────────────────────
// ToolUseSummaryMessage
// ─────────────────────────────────────────────────────────────────────────────

// ToolUseSummaryMessage is a brief human-readable summary of tool activity
// generated asynchronously (by a lighter model) while the main loop runs.
type ToolUseSummaryMessage struct {
	Type      MessageType `json:"type"`
	UUID      string      `json:"uuid"`
	Timestamp string      `json:"timestamp"`
	Summary   string      `json:"summary"`
}

func (m *ToolUseSummaryMessage) GetType()      MessageType { return MessageTypeToolUseSummary }
func (m *ToolUseSummaryMessage) GetUUID()      string       { return m.UUID }
func (m *ToolUseSummaryMessage) GetTimestamp() string       { return m.Timestamp }

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// IsCompactBoundary reports whether m is a compact_boundary system message.
func IsCompactBoundary(m Message) bool {
	sm, ok := m.(*SystemMessage)
	return ok && sm.Subtype == SystemSubtypeCompactBoundary
}

// GetMessagesAfterCompactBoundary returns only the messages that follow the
// last compact_boundary marker (the "live" context window).
func GetMessagesAfterCompactBoundary(messages []Message) []Message {
	lastBoundary := -1
	for i, m := range messages {
		if IsCompactBoundary(m) {
			lastBoundary = i
		}
	}
	if lastBoundary == -1 {
		return messages
	}
	return messages[lastBoundary+1:]
}
