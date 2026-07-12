package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Engine routes messages between a single IM platform and the omp agent.
type Engine struct {
	agent     Agent
	platforms []Platform

	ctx    context.Context
	cancel context.CancelFunc

	sessions   map[string]AgentSession
	sessionsMu sync.Mutex

	// MaxHistoryTurns limits how many user/assistant exchanges are kept
	// in a session's implicit context. 0 means unlimited.
	MaxHistoryTurns int
}

// NewEngine creates an engine with the given agent.
func NewEngine(agent Agent) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		agent:           agent,
		platforms:       make([]Platform, 0),
		ctx:             ctx,
		cancel:          cancel,
		sessions:        make(map[string]AgentSession),
		MaxHistoryTurns: 20,
	}
}

// AddPlatform registers a platform. Platforms must be added before Run.
func (e *Engine) AddPlatform(p Platform) {
	e.platforms = append(e.platforms, p)
}

// Run starts all platforms and blocks until Stop is called.
func (e *Engine) Run() error {
	var wg sync.WaitGroup
	for _, p := range e.platforms {
		wg.Add(1)
		go func(p Platform) {
			defer wg.Done()
			if err := p.Start(e.handleMessage); err != nil {
				slog.Error("platform stopped", "platform", p.Name(), "error", err)
			}
		}(p)
	}
	wg.Wait()
	return nil
}

// Stop shuts down the engine and all active sessions.
func (e *Engine) Stop() error {
	e.cancel()
	for _, p := range e.platforms {
		if err := p.Stop(); err != nil {
			slog.Warn("platform stop error", "platform", p.Name(), "error", err)
		}
	}
	e.sessionsMu.Lock()
	for k, s := range e.sessions {
		if err := s.Close(); err != nil {
			slog.Warn("session close error", "session", k, "error", err)
		}
	}
	e.sessions = make(map[string]AgentSession)
	e.sessionsMu.Unlock()
	return e.agent.Stop()
}

func (e *Engine) handleMessage(p Platform, msg *Message) {
	if err := e.ctx.Err(); err != nil {
		return
	}
	slog.Info("incoming message", "platform", msg.Platform, "session", msg.SessionKey, "user", msg.UserID)

	ctx, cancel := context.WithTimeout(e.ctx, defaultTurnTimeout)
	defer cancel()

	session, err := e.getOrCreateSession(ctx, msg.SessionKey)
	if err != nil {
		slog.Error("failed to start session", "session", msg.SessionKey, "error", err)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("无法启动会话: %v", err))
		return
	}

	reply, attachments, err := session.Respond(ctx, msg.Content, msg.Images)
	if err != nil {
		slog.Error("agent respond error", "session", msg.SessionKey, "error", err)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("处理失败: %v", err))
		return
	}

	if err := p.Reply(ctx, msg.ReplyCtx, reply); err != nil {
		slog.Error("failed to send reply", "session", msg.SessionKey, "error", err)
	}

	for _, a := range attachments {
		if len(a.Data) == 0 {
			continue
		}
		switch a.Kind {
		case "image":
			s, ok := p.(ImageSender)
			if !ok {
				slog.Warn("platform does not support sending images", "platform", p.Name())
				continue
			}
			if err := s.SendImage(ctx, msg.ReplyCtx, ImageAttachment{FileName: a.FileName, MimeType: a.MimeType, Data: a.Data}); err != nil {
				slog.Error("failed to send image", "session", msg.SessionKey, "error", err)
			}
		case "file":
			s, ok := p.(FileSender)
			if !ok {
				slog.Warn("platform does not support sending files", "platform", p.Name())
				continue
			}
			if err := s.SendFile(ctx, msg.ReplyCtx, FileAttachment{FileName: a.FileName, MimeType: a.MimeType, Data: a.Data}); err != nil {
				slog.Error("failed to send file", "session", msg.SessionKey, "error", err)
			}
		default:
			slog.Warn("unknown outbound attachment kind", "kind", a.Kind)
		}
	}
}

func (e *Engine) getOrCreateSession(ctx context.Context, sessionKey string) (AgentSession, error) {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()

	if s, ok := e.sessions[sessionKey]; ok {
		return s, nil
	}

	s, err := e.agent.StartSession(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	e.sessions[sessionKey] = s
	return s, nil
}
