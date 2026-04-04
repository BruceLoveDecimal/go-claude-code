package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/claude-code/go-claude-go/types"
)

// AppendMessages appends one or more messages to the session JSONL file.
// Each message is serialised as a single JSON line.  The file is created if
// it does not exist.
//
// On the very first call for a session, a header line containing SessionMeta
// is written before the messages.  Subsequent calls just append.
func AppendMessages(sessionID string, meta SessionMeta, msgs []types.Message) error {
	if len(msgs) == 0 {
		return nil
	}

	path, err := SessionFilePath(sessionID)
	if err != nil {
		return fmt.Errorf("session path: %w", err)
	}

	// Open for append-only; create if not present.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	// Write header on first creation (file size == 0).
	info, _ := f.Stat()
	if info != nil && info.Size() == 0 {
		header := map[string]interface{}{
			"_type":      "session_meta",
			"session_id": meta.SessionID,
			"created_at": meta.CreatedAt,
			"model":      meta.Model,
		}
		b, err := json.Marshal(header)
		if err != nil {
			return fmt.Errorf("marshal session header: %w", err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			return fmt.Errorf("write session header: %w", err)
		}
	}

	w := bufio.NewWriter(f)
	for _, m := range msgs {
		b, err := types.MarshalMessage(m)
		if err != nil {
			return fmt.Errorf("marshal message %s: %w", m.GetUUID(), err)
		}
		if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
			return fmt.Errorf("write message: %w", err)
		}
	}
	return w.Flush()
}

// LoadSession reads all conversation messages from the JSONL file for the
// given sessionID.  The metadata header line is skipped.  Returns an empty
// slice if the session file does not exist yet.
func LoadSession(sessionID string) ([]types.Message, error) {
	path, err := SessionFilePath(sessionID)
	if err != nil {
		return nil, fmt.Errorf("session path: %w", err)
	}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	var messages []types.Message
	scanner := bufio.NewScanner(f)
	// Increase buffer size for large assistant messages.
	scanner.Buffer(make([]byte, 0, 1*1024*1024), 10*1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Skip the metadata header line.
		var peek struct {
			Type string `json:"_type"`
		}
		if err := json.Unmarshal(line, &peek); err == nil && peek.Type == "session_meta" {
			continue
		}

		msg, err := types.UnmarshalMessage(line)
		if err != nil {
			// Log and skip malformed lines rather than aborting the whole load.
			continue
		}
		messages = append(messages, msg)
	}

	if err := scanner.Err(); err != nil {
		return messages, fmt.Errorf("scan session file line %d: %w", lineNum, err)
	}
	return messages, nil
}

// ListSessions returns the session IDs of all saved sessions, most recent
// first (sorted by file modification time).
func ListSessions() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := fmt.Sprintf("%s/%s", home, sessionDir)

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	type entry struct {
		id    string
		modAt int64
	}
	var sessions []entry
	for _, e := range entries {
		name := e.Name()
		if len(name) <= 5 || name[len(name)-5:] != ".jsonl" {
			continue
		}
		id := name[:len(name)-6] // strip ".jsonl"
		info, _ := e.Info()
		var mod int64
		if info != nil {
			mod = info.ModTime().UnixNano()
		}
		sessions = append(sessions, entry{id: id, modAt: mod})
	}

	// Sort most-recent first.
	for i := 0; i < len(sessions)-1; i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[j].modAt > sessions[i].modAt {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}

	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.id
	}
	return ids, nil
}
