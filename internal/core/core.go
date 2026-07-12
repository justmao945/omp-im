// Package core provides the minimal abstractions that wire an IM platform
// to one of several agent backends.
package core

import (
	"context"
	"errors"
	"time"
)

// Project identifies a working directory for an agent session.
type Project struct {
	Name    string
	WorkDir string
}

// Platform abstracts a messaging platform (Weixin, WeCom, etc.).
type Platform interface {
	Name() string
	Start(handler MessageHandler) error
	Stop() error

	// Reply sends text back to the user identified by replyCtx.
	Reply(ctx context.Context, replyCtx any, content string) error
}

// MessageHandler is called by a platform when a new user message arrives.
type MessageHandler func(p Platform, msg *Message)

// Agent abstracts an agent backend (omp, claude, codex, etc.).
type Agent interface {
	Name() string
	// StartSession creates a session for the given conversation key and project.
	// If resumeSessionID is non-empty, the agent should attempt to resume that
	// session; otherwise it creates a new one.
	StartSession(ctx context.Context, sessionKey string, project Project, resumeSessionID string) (AgentSession, error)
	Stop() error
}

// HistoryEntry describes a single message in the session's conversation context.
type HistoryEntry struct {
	Role    string
	Content string
}

// AgentStatus describes the current state of an agent turn.
type AgentStatus struct {
	State               string
	TurnDuration        time.Duration
	ToolCount           int
	CurrentToolDuration time.Duration
	CurrentToolCommand  string
	InputTokens         int
	OutputTokens        int
	Model               string
	ReasoningEffort     string
}

// AgentSession is a single running conversation with an agent.
type AgentSession interface {
	// Respond sends the current conversation turn to the agent and returns
	// the assistant's text reply along with any files/images the agent produced.
	Respond(ctx context.Context, prompt string, images []ImageAttachment) (string, []OutboundAttachment, error)
	// Status returns the current state of the session (idle, thinking, using_tools, etc.)
	// along with turn timing and usage information.
	Status() AgentStatus
	// History returns the conversation context retained by the session.
	History() []HistoryEntry
	Close() error
	// SessionID returns the agent-side session identifier, used to resume the
	// conversation after a restart.
	SessionID() string
}

// ImageSender is implemented by platforms that can send images.
type ImageSender interface {
	SendImage(ctx context.Context, replyCtx any, img ImageAttachment) error
}

// FileSender is implemented by platforms that can send files.
type FileSender interface {
	SendFile(ctx context.Context, replyCtx any, file FileAttachment) error
}

// ErrNotSupported is returned by optional platform/agent operations.
var ErrNotSupported = errors.New("operation not supported")
