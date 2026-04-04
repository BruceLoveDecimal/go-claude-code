package types

import (
	"encoding/json"
	"fmt"
)

// MarshalMessage serialises any Message to JSON.  Because all concrete message
// types carry proper json tags, a plain json.Marshal is sufficient.
func MarshalMessage(m Message) ([]byte, error) {
	return json.Marshal(m)
}

// UnmarshalMessage deserialises a JSON object produced by MarshalMessage back
// into a typed Message.  It peeks at the "type" discriminant field and
// delegates to the appropriate concrete type.
func UnmarshalMessage(data []byte) (Message, error) {
	var header struct {
		Type MessageType `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("unmarshal message header: %w", err)
	}

	switch header.Type {
	case MessageTypeUser:
		var m UserMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal UserMessage: %w", err)
		}
		return &m, nil

	case MessageTypeAssistant:
		var m AssistantMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal AssistantMessage: %w", err)
		}
		// APIMessage stores content blocks in RawContent; decode them now so
		// callers get a fully hydrated message.
		if err := m.Msg.DecodeContent(); err != nil {
			return nil, fmt.Errorf("decode AssistantMessage content: %w", err)
		}
		return &m, nil

	case MessageTypeSystem:
		var m SystemMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal SystemMessage: %w", err)
		}
		return &m, nil

	case MessageTypeTombstone:
		var m TombstoneMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal TombstoneMessage: %w", err)
		}
		return &m, nil

	case MessageTypeToolUseSummary:
		var m ToolUseSummaryMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal ToolUseSummaryMessage: %w", err)
		}
		return &m, nil

	default:
		return nil, fmt.Errorf("unknown message type %q", header.Type)
	}
}
