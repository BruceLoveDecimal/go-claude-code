// Package session provides JSONL-based conversation history persistence and
// resume support.  Each session maps to a single file at
// ~/.claude-go/sessions/<sessionID>.jsonl where every line is one serialised
// types.Message.
package session

import (
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

const sessionDir = ".claude-go/sessions"

// NewSessionID generates a fresh unique session identifier.
func NewSessionID() string {
	return uuid.New().String()
}

// SessionFilePath returns the absolute path for the given sessionID.
// The directory (~/.claude-go/sessions/) is created if it does not exist.
func SessionFilePath(sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, sessionDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

// SessionMeta holds lightweight metadata stored at the start of the JSONL
// file so sessions can be listed without reading all messages.
type SessionMeta struct {
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
	Model     string    `json:"model,omitempty"`
}

// NewSessionMeta creates a SessionMeta with the current time.
func NewSessionMeta(sessionID, model string) SessionMeta {
	return SessionMeta{
		SessionID: sessionID,
		CreatedAt: time.Now().UTC(),
		Model:     model,
	}
}
