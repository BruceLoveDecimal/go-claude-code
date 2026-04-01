package types

import "encoding/json"

// ContentBlockType identifies the kind of content block.
type ContentBlockType string

const (
	ContentTypeText        ContentBlockType = "text"
	ContentTypeToolUse     ContentBlockType = "tool_use"
	ContentTypeToolResult  ContentBlockType = "tool_result"
	ContentTypeThinking    ContentBlockType = "thinking"
	ContentTypeRedacted    ContentBlockType = "redacted_thinking"
	ContentTypeImage       ContentBlockType = "image"
)

// ContentBlock is the discriminated union for all content block kinds.
type ContentBlock interface {
	GetBlockType() ContentBlockType
}

// TextBlock holds plain text from the model.
type TextBlock struct {
	Type ContentBlockType `json:"type"`
	Text string           `json:"text"`
}

func (b *TextBlock) GetBlockType() ContentBlockType { return ContentTypeText }

// ToolUseBlock is emitted by the model to request a tool call.
type ToolUseBlock struct {
	Type  ContentBlockType `json:"type"`
	ID    string           `json:"id"`
	Name  string           `json:"name"`
	Input json.RawMessage  `json:"input"`
}

func (b *ToolUseBlock) GetBlockType() ContentBlockType { return ContentTypeToolUse }

// InputMap parses the raw JSON input into a map.
func (b *ToolUseBlock) InputMap() (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(b.Input, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ToolResultBlock carries the result of a tool back to the model.
type ToolResultBlock struct {
	Type      ContentBlockType `json:"type"`
	ToolUseID string           `json:"tool_use_id"`
	Content   string           `json:"content"`
	IsError   bool             `json:"is_error,omitempty"`
}

func (b *ToolResultBlock) GetBlockType() ContentBlockType { return ContentTypeToolResult }

// ThinkingBlock contains the model's internal reasoning (extended thinking).
type ThinkingBlock struct {
	Type     ContentBlockType `json:"type"`
	Thinking string           `json:"thinking"`
}

func (b *ThinkingBlock) GetBlockType() ContentBlockType { return ContentTypeThinking }

// RedactedThinkingBlock is an opaque thinking block that must be preserved as-is.
type RedactedThinkingBlock struct {
	Type ContentBlockType `json:"type"`
	Data string           `json:"data"`
}

func (b *RedactedThinkingBlock) GetBlockType() ContentBlockType { return ContentTypeRedacted }

// UnmarshalContentBlock deserializes a raw JSON content block by type discriminant.
func UnmarshalContentBlock(raw json.RawMessage) (ContentBlock, error) {
	var disc struct {
		Type ContentBlockType `json:"type"`
	}
	if err := json.Unmarshal(raw, &disc); err != nil {
		return nil, err
	}
	switch disc.Type {
	case ContentTypeText:
		var b TextBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return &b, nil
	case ContentTypeToolUse:
		var b ToolUseBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return &b, nil
	case ContentTypeToolResult:
		var b ToolResultBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return &b, nil
	case ContentTypeThinking:
		var b ThinkingBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return &b, nil
	case ContentTypeRedacted:
		var b RedactedThinkingBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return &b, nil
	default:
		// Unknown block type — return as raw text for forward-compat
		return &TextBlock{Type: disc.Type, Text: string(raw)}, nil
	}
}

// Usage tracks token consumption for a single API call.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// Add accumulates token counts from another Usage.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:              u.InputTokens + other.InputTokens,
		OutputTokens:             u.OutputTokens + other.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens + other.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens + other.CacheReadInputTokens,
	}
}
