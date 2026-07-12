// Package core provides the minimal abstractions that wire an IM platform
// to the omp agent. It is intentionally much smaller than cc-connect's core:
// only one agent (omp) and one platform at a time is required.
package core

import (
	"context"
	"errors"
)

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

// Agent abstracts the omp agent backend.
type Agent interface {
	Name() string
	// StartSession creates a session for the given conversation key.
	StartSession(ctx context.Context, sessionKey string) (AgentSession, error)
	Stop() error
}

// AgentSession is a single running conversation with the omp agent.
type AgentSession interface {
	// Respond sends the current conversation turn to the agent and returns
	// the assistant's text reply along with any files/images the agent produced.
	Respond(ctx context.Context, prompt string, images []ImageAttachment) (string, []OutboundAttachment, error)
	Close() error
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
