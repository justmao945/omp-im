package core

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine routes messages between IM platforms and a configurable set of agents.
type Engine struct {
	agents         map[string]Agent
	defaultAgent   string
	projects       map[string]Project
	defaultProject string
	platforms      []Platform

	ctx    context.Context
	cancel context.CancelFunc

	sessions   map[string]*sessionEntry
	sessionsMu sync.Mutex

	// MaxHistoryTurns limits how many user/assistant exchanges are kept
	// in a session's implicit context. 0 means unlimited.
	MaxHistoryTurns int
}

type sessionEntry struct {
	session      AgentSession
	agent        string
	project      string
	status       string
	lastActivity time.Time
}

// NewEngine creates an engine with the given agents and projects.
func NewEngine(agents map[string]Agent, defaultAgent string, projects map[string]Project, defaultProject string) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		agents:          agents,
		defaultAgent:    defaultAgent,
		projects:        projects,
		defaultProject:  defaultProject,
		platforms:       make([]Platform, 0),
		ctx:             ctx,
		cancel:          cancel,
		sessions:        make(map[string]*sessionEntry),
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
		if s.session != nil {
			if err := s.session.Close(); err != nil {
				slog.Warn("session close error", "session", k, "error", err)
			}
		}
	}
	e.sessions = make(map[string]*sessionEntry)
	e.sessionsMu.Unlock()

	for _, a := range e.agents {
		if err := a.Stop(); err != nil {
			slog.Warn("agent stop error", "agent", a.Name(), "error", err)
		}
	}
	return nil
}

func (e *Engine) handleMessage(p Platform, msg *Message) {
	if err := e.ctx.Err(); err != nil {
		return
	}
	slog.Info("incoming message", "platform", msg.Platform, "session", msg.SessionKey, "user", msg.UserID)

	ctx, cancel := context.WithTimeout(e.ctx, defaultTurnTimeout)
	defer cancel()

	cmd, isCmd := parseCommand(msg.Content)
	if isCmd {
		e.handleCommand(ctx, p, msg, cmd)
		return
	}

	e.sessionsMu.Lock()
	e.touchSessionLocked(msg.SessionKey)
	e.sessionsMu.Unlock()

	session, err := e.getOrCreateSession(ctx, msg.SessionKey)
	if err != nil {
		slog.Error("failed to start session", "session", msg.SessionKey, "error", err)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("无法启动会话: %v", err))
		return
	}

	e.setSessionStatus(msg.SessionKey, "busy")
	reply, attachments, err := session.Respond(ctx, msg.Content, msg.Images)
	e.setSessionStatus(msg.SessionKey, "idle")
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
	ent, ok := e.sessions[sessionKey]
	if !ok {
		ent = &sessionEntry{
			agent:        e.defaultAgent,
			project:      e.defaultProject,
			status:       "idle",
			lastActivity: time.Now(),
		}
		e.sessions[sessionKey] = ent
	}
	if ent.session != nil {
		ent.lastActivity = time.Now()
		e.sessionsMu.Unlock()
		return ent.session, nil
	}
	agentName := ent.agent
	projectName := ent.project
	e.sessionsMu.Unlock()

	agent, ok := e.agents[agentName]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentName)
	}
	project, ok := e.projects[projectName]
	if !ok {
		return nil, fmt.Errorf("unknown project %q", projectName)
	}

	s, err := agent.StartSession(ctx, sessionKey, project)
	if err != nil {
		e.sessionsMu.Lock()
		if ent, ok := e.sessions[sessionKey]; ok {
			ent.status = "error"
		}
		e.sessionsMu.Unlock()
		return nil, err
	}

	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	if existing, ok := e.sessions[sessionKey]; ok && existing.session != nil {
		_ = s.Close()
		return existing.session, nil
	}
	ent.session = s
	ent.status = "idle"
	ent.lastActivity = time.Now()
	return s, nil
}

func (e *Engine) setSessionStatus(sessionKey, status string) {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	if ent, ok := e.sessions[sessionKey]; ok {
		ent.status = status
		ent.lastActivity = time.Now()
	}
}

func (e *Engine) touchSessionLocked(sessionKey string) {
	if ent, ok := e.sessions[sessionKey]; ok {
		ent.lastActivity = time.Now()
	}
}

type command struct {
	name string
	arg  string
}

func parseCommand(s string) (command, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/") {
		return command{}, false
	}
	parts := strings.Fields(s)
	name := strings.TrimPrefix(parts[0], "/")
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	return command{name: name, arg: arg}, true
}

func (e *Engine) handleCommand(ctx context.Context, p Platform, msg *Message, cmd command) {
	switch cmd.name {
	case "agent":
		e.handleAgentCommand(ctx, p, msg, cmd.arg)
	case "proj":
		e.handleProjCommand(ctx, p, msg, cmd.arg)
	case "list":
		e.handleListCommand(ctx, p, msg)
	default:
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("未知命令: /%s", cmd.name))
	}
}

func (e *Engine) handleAgentCommand(ctx context.Context, p Platform, msg *Message, arg string) {
	sessionKey := msg.SessionKey
	if arg == "" {
		current := e.sessionAgent(sessionKey)
		var names []string
		for name := range e.agents {
			names = append(names, name)
		}
		sort.Strings(names)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("当前 agent: %s\n可用: %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.agents[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("未知 agent: %s", arg))
		return
	}
	e.setSessionAgent(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("已切换 agent 为 %s，下条消息生效", arg))
}

func (e *Engine) handleProjCommand(ctx context.Context, p Platform, msg *Message, arg string) {
	sessionKey := msg.SessionKey
	if arg == "" {
		current := e.sessionProject(sessionKey)
		var names []string
		for name := range e.projects {
			names = append(names, name)
		}
		sort.Strings(names)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("当前 project: %s\n可用: %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.projects[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("未知 project: %s", arg))
		return
	}
	e.setSessionProject(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("已切换 project 为 %s，下条消息生效", arg))
}

func (e *Engine) handleListCommand(ctx context.Context, p Platform, msg *Message) {
	currentAgent := e.sessionAgent(msg.SessionKey)
	agent, ok := e.agents[currentAgent]
	if !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("未知 agent: %s", currentAgent))
		return
	}
	infos, err := agent.ListSessions(ctx)
	if err != nil {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("无法读取 sessions: %v", err))
		return
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Agent %s 的 sessions:", currentAgent))
	for _, info := range infos {
		status := info.Status
		if status == "" {
			status = "idle"
		}
		proj := info.Project
		if proj == "" {
			proj = e.defaultProject
		}
		lines = append(lines, fmt.Sprintf("- %s [project=%s status=%s last=%s]", info.SessionKey, proj, status, info.LastActivity.Format("15:04:05")))
	}
	if len(lines) == 1 {
		lines = append(lines, "（无）")
	}
	_ = p.Reply(ctx, msg.ReplyCtx, strings.Join(lines, "\n"))
}

func (e *Engine) sessionAgent(sessionKey string) string {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	if ent, ok := e.sessions[sessionKey]; ok && ent.agent != "" {
		return ent.agent
	}
	return e.defaultAgent
}

func (e *Engine) sessionProject(sessionKey string) string {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	if ent, ok := e.sessions[sessionKey]; ok && ent.project != "" {
		return ent.project
	}
	return e.defaultProject
}

func (e *Engine) setSessionAgent(sessionKey, agentName string) {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	ent, ok := e.sessions[sessionKey]
	if !ok {
		ent = &sessionEntry{
			agent:        agentName,
			project:      e.defaultProject,
			status:       "idle",
			lastActivity: time.Now(),
		}
		e.sessions[sessionKey] = ent
		return
	}
	if ent.agent == agentName {
		return
	}
	if ent.session != nil {
		_ = ent.session.Close()
	}
	ent.session = nil
	ent.agent = agentName
	ent.status = "idle"
	ent.lastActivity = time.Now()
}

func (e *Engine) setSessionProject(sessionKey, projectName string) {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	ent, ok := e.sessions[sessionKey]
	if !ok {
		ent = &sessionEntry{
			agent:        e.defaultAgent,
			project:      projectName,
			status:       "idle",
			lastActivity: time.Now(),
		}
		e.sessions[sessionKey] = ent
		return
	}
	if ent.project == projectName {
		return
	}
	if ent.session != nil {
		_ = ent.session.Close()
	}
	ent.session = nil
	ent.project = projectName
	ent.status = "idle"
	ent.lastActivity = time.Now()
}
