package api

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SSE raw event types (Anthropic API)
// ─────────────────────────────────────────────────────────────────────────────

// sseEvent is the envelope for each server-sent event line.
type sseEvent struct {
	Type  string          `json:"type"`
	Index int             `json:"index,omitempty"`
	Delta *sseDelta       `json:"delta,omitempty"`
	// message_start
	Message *sseMessage `json:"message,omitempty"`
	// content_block_start
	ContentBlock *sseContentBlock `json:"content_block,omitempty"`
	// error event
	Error *sseErrorBody `json:"error,omitempty"`
}

// sseErrorBody is the "error" field inside an SSE error event.
type sseErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type sseDelta struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

type sseMessage struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	StopReason string          `json:"stop_reason"`
	Type       string          `json:"type"`
	Usage      types.Usage     `json:"usage"`
}

type sseContentBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Text  string `json:"text,omitempty"`
	Data  string `json:"data,omitempty"` // redacted_thinking opaque blob
}

// ─────────────────────────────────────────────────────────────────────────────
// Streaming assembler
// ─────────────────────────────────────────────────────────────────────────────

// streamAssembler collects deltas from the SSE stream and emits a complete
// AssistantMessage once all blocks have been accumulated.
type streamAssembler struct {
	// API-level message metadata
	msgID      string
	model      string
	stopReason string
	usage      types.Usage

	// Per-block accumulators keyed by block index
	textBlocks            map[int]*textAccum
	toolUseBlocks         map[int]*toolAccum
	thinkingBlocks        map[int]*thinkingAccum
	redactedThinkingBlocks map[int]*redactedThinkingAccum
	blockOrder            []int // insertion order for deterministic output
}

type textAccum struct {
	text string
}

type toolAccum struct {
	id          string
	name        string
	inputBuffer strings.Builder
}

type thinkingAccum struct {
	thinking string
}

type redactedThinkingAccum struct {
	data string
}

func newStreamAssembler() *streamAssembler {
	return &streamAssembler{
		textBlocks:             make(map[int]*textAccum),
		toolUseBlocks:          make(map[int]*toolAccum),
		thinkingBlocks:         make(map[int]*thinkingAccum),
		redactedThinkingBlocks: make(map[int]*redactedThinkingAccum),
	}
}

// applyEvent updates internal state for one parsed SSE event.
// Returns (deltaMsg, isComplete) where deltaMsg is non-nil when there's an
// incremental update worth propagating (StreamDeltaEvent or BlockCompleteEvent)
// and isComplete is true when the message is fully assembled.
func (a *streamAssembler) applyEvent(ev sseEvent) (deltaMsg interface{}, isComplete bool) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.msgID = ev.Message.ID
			a.model = ev.Message.Model
			a.usage = ev.Message.Usage
		}

	case "content_block_start":
		if ev.ContentBlock == nil {
			break
		}
		idx := ev.Index
		// Track insertion order for deterministic output
		a.blockOrder = append(a.blockOrder, idx)

		switch ev.ContentBlock.Type {
		case "text":
			a.textBlocks[idx] = &textAccum{text: ev.ContentBlock.Text}
		case "tool_use":
			a.toolUseBlocks[idx] = &toolAccum{
				id:   ev.ContentBlock.ID,
				name: ev.ContentBlock.Name,
			}
		case "thinking":
			a.thinkingBlocks[idx] = &thinkingAccum{thinking: ev.ContentBlock.Text}
		case "redacted_thinking":
			a.redactedThinkingBlocks[idx] = &redactedThinkingAccum{data: ev.ContentBlock.Data}
		}

	case "content_block_delta":
		if ev.Delta == nil {
			break
		}
		idx := ev.Index
		switch ev.Delta.Type {
		case "text_delta":
			if acc, ok := a.textBlocks[idx]; ok {
				acc.text += ev.Delta.Text
			}
			return &types.StreamDeltaEvent{
				Type:      "stream_delta",
				DeltaType: "text_delta",
				Index:     idx,
				Text:      ev.Delta.Text,
			}, false
		case "input_json_delta":
			if acc, ok := a.toolUseBlocks[idx]; ok {
				acc.inputBuffer.WriteString(ev.Delta.PartialJSON)
			}
			return &types.StreamDeltaEvent{
				Type:      "stream_delta",
				DeltaType: "input_json_delta",
				Index:     idx,
				InputJSON: ev.Delta.PartialJSON,
			}, false
		case "thinking_delta":
			if acc, ok := a.thinkingBlocks[idx]; ok {
				acc.thinking += ev.Delta.Thinking
			}
		}

	case "content_block_stop":
		// Emit a BlockCompleteEvent so the streaming tool executor can start
		// executing completed tool_use blocks before the full response.
		idx := ev.Index
		if acc, ok := a.toolUseBlocks[idx]; ok {
			inputJSON := acc.inputBuffer.String()
			if inputJSON == "" {
				inputJSON = "{}"
			}
			block := &types.ToolUseBlock{
				Type:  types.ContentTypeToolUse,
				ID:    acc.id,
				Name:  acc.name,
				Input: json.RawMessage(inputJSON),
			}
			return &types.BlockCompleteEvent{
				Type:         "block_complete",
				Index:        idx,
				ToolUseBlock: block,
			}, false
		}

	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			a.stopReason = ev.Delta.StopReason
		}

	case "message_stop":
		return nil, true
	}
	return nil, false
}

// build assembles the final AssistantMessage from accumulated blocks.
func (a *streamAssembler) build() *types.AssistantMessage {
	var content []types.ContentBlock

	for _, idx := range a.blockOrder {
		if acc, ok := a.textBlocks[idx]; ok {
			content = append(content, &types.TextBlock{
				Type: types.ContentTypeText,
				Text: acc.text,
			})
		} else if acc, ok := a.toolUseBlocks[idx]; ok {
			inputJSON := acc.inputBuffer.String()
			if inputJSON == "" {
				inputJSON = "{}"
			}
			content = append(content, &types.ToolUseBlock{
				Type:  types.ContentTypeToolUse,
				ID:    acc.id,
				Name:  acc.name,
				Input: json.RawMessage(inputJSON),
			})
		} else if acc, ok := a.thinkingBlocks[idx]; ok {
			content = append(content, &types.ThinkingBlock{
				Type:     types.ContentTypeThinking,
				Thinking: acc.thinking,
			})
		} else if acc, ok := a.redactedThinkingBlocks[idx]; ok {
			content = append(content, &types.RedactedThinkingBlock{
				Type: types.ContentTypeRedacted,
				Data: acc.data,
			})
		}
	}

	return &types.AssistantMessage{
		Type:      types.MessageTypeAssistant,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Msg: types.APIMessage{
			ID:         a.msgID,
			Model:      a.model,
			Role:       "assistant",
			StopReason: a.stopReason,
			Type:       "message",
			Usage:      a.usage,
			Content:    content,
		},
	}
}
