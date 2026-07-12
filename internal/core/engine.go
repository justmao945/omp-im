package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
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

	sessionStore     sessionStore
	sessionStorePath string
	sessionStoreMu   sync.Mutex

	activeTurns   map[string]context.CancelFunc
	activeTurnsMu sync.Mutex
}

type queuedMessage struct {
	p   Platform
	msg *Message
}

type sessionEntry struct {
	session      AgentSession
	agent        string
	project      string
	status       string
	lastActivity time.Time

	// queue holds normal messages waiting to be processed in order for this
	// session. A single worker goroutine drains the queue and exits when it is
	// empty, so ordering is preserved even when the platform dispatches
	// messages concurrently.
	queue      []*queuedMessage
	processing bool
	closed     bool
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
		activeTurns:     make(map[string]context.CancelFunc),
	}
}

// SetSessionStore configures the path used to persist session IDs across
// restarts and loads any previously saved IDs from that file.
func (e *Engine) SetSessionStore(path string) error {
	e.sessionStoreMu.Lock()
	defer e.sessionStoreMu.Unlock()
	if e.sessionStore != nil {
		_ = e.sessionStore.Close()
	}
	e.sessionStorePath = path
	store, err := newSessionStore(path)
	if err != nil {
		return err
	}
	e.sessionStore = store
	return nil
}

func (e *Engine) loadSessionStore() error {
	// Deprecated: no-op because each operation now reads/writes the store directly.
	return nil
}

func (e *Engine) saveSessionStore() error {
	// Deprecated: no-op because each operation now writes the store directly.
	return nil
}

func (e *Engine) getSessionID(sessionKey string) (string, error) {
	e.sessionStoreMu.Lock()
	defer e.sessionStoreMu.Unlock()
	if e.sessionStore == nil {
		return "", nil
	}
	return e.sessionStore.Get(sessionKey)
}

func (e *Engine) setSessionID(sessionKey, sessionID string) {
	if sessionKey == "" || sessionID == "" {
		return
	}
	e.sessionStoreMu.Lock()
	defer e.sessionStoreMu.Unlock()
	if e.sessionStore == nil {
		return
	}
	if err := e.sessionStore.Set(sessionKey, sessionID); err != nil {
		slog.Error("failed to save session store", "error", err)
	}
}

func (e *Engine) deleteSessionID(sessionKey string) {
	e.sessionStoreMu.Lock()
	defer e.sessionStoreMu.Unlock()
	if e.sessionStore == nil {
		return
	}
	if err := e.sessionStore.Delete(sessionKey); err != nil {
		slog.Error("failed to delete session from store", "error", err)
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
	for _, ent := range e.sessions {
		ent.closed = true
		ent.queue = nil
		if ent.session != nil {
			if err := ent.session.Close(); err != nil {
				slog.Warn("session close error", "error", err)
			}
		}
	}
	e.sessions = make(map[string]*sessionEntry)
	e.sessionsMu.Unlock()

	e.sessionStoreMu.Lock()
	if e.sessionStore != nil {
		_ = e.sessionStore.Close()
		e.sessionStore = nil
	}
	e.sessionStoreMu.Unlock()

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
	if msg.UserID == "" {
		return
	}
	slog.Info("incoming message", "platform", msg.Platform, "session", msg.SessionKey, "user", msg.UserID, "content", truncate(msg.Content, 200))

	cmd, isCmd := parseCommand(msg.Content)
	if isCmd {
		ctx, cancel := context.WithTimeout(e.ctx, defaultTurnTimeout)
		defer cancel()
		e.handleCommand(ctx, p, msg, cmd)
		return
	}

	e.queueNormalMessage(p, msg)
}

func (e *Engine) queueNormalMessage(p Platform, msg *Message) {
	e.sessionsMu.Lock()
	ent, ok := e.sessions[msg.SessionKey]
	if !ok || ent.closed {
		ent = &sessionEntry{
			agent:        e.defaultAgent,
			project:      e.defaultProject,
			status:       "idle",
			lastActivity: time.Now(),
		}
		e.sessions[msg.SessionKey] = ent
	}
	ent.lastActivity = time.Now()
	ent.queue = append(ent.queue, &queuedMessage{p: p, msg: msg})
	if !ent.processing {
		ent.processing = true
		go e.sessionWorker(msg.SessionKey)
	}
	e.sessionsMu.Unlock()
}

func (e *Engine) sessionWorker(sessionKey string) {
	for {
		e.sessionsMu.Lock()
		ent, ok := e.sessions[sessionKey]
		if !ok || ent == nil || ent.closed || len(ent.queue) == 0 {
			if ok && ent != nil {
				ent.processing = false
			}
			e.sessionsMu.Unlock()
			return
		}
		qm := ent.queue[0]
		ent.queue = ent.queue[1:]
		e.sessionsMu.Unlock()

		ctx, cancel := context.WithTimeout(e.ctx, defaultTurnTimeout)
		e.processNormalMessage(ctx, cancel, qm.p, qm.msg, ent)
		cancel()
	}
}

func (e *Engine) processNormalMessage(ctx context.Context, cancel context.CancelFunc, p Platform, msg *Message, ent *sessionEntry) {
	if err := e.ensureSessionForEntry(ctx, ent, msg.SessionKey); err != nil {
		slog.Error("failed to start session", "session", msg.SessionKey, "error", err)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Failed to start session: %v", err))
		return
	}

	e.sessionsMu.Lock()
	if ent.session == nil {
		e.sessionsMu.Unlock()
		return
	}
	ent.lastActivity = time.Now()
	Session := ent.session
	e.sessionsMu.Unlock()

	slog.Info("agent turn started", "session", msg.SessionKey, "agent", e.sessionAgent(msg.SessionKey), "project", e.sessionProject(msg.SessionKey))
	e.setSessionStatus(msg.SessionKey, "busy")
	e.activeTurnsMu.Lock()
	e.activeTurns[msg.SessionKey] = cancel
	e.activeTurnsMu.Unlock()

	var reply string
	var attachments []OutboundAttachment
	var err error

	if streamer, ok := p.(StreamReplyer); ok {
		onEvent := func(ev StreamEvent) {
			switch ev.Type {
			case "text":
				if err := streamer.StreamReply(ctx, msg.ReplyCtx, ev.Text, false); err != nil {
					slog.Error("failed to stream reply", "session", msg.SessionKey, "error", err)
				}
			case "thinking", "tool_start", "tool_end", "processing", "usage":
				if err := streamer.StreamEvent(ctx, msg.ReplyCtx, ev); err != nil {
					slog.Error("failed to stream event", "session", msg.SessionKey, "error", err)
				}
			}
		}
		onEvent(StreamEvent{Type: "processing"})
		reply, attachments, err = Session.Respond(ctx, msg.Content, msg.Images, msg.Files, onEvent)
		if err == nil {
			if err := streamer.StreamReply(ctx, msg.ReplyCtx, "", true); err != nil {
				slog.Error("failed to finish stream reply", "session", msg.SessionKey, "error", err)
			}
		}
	} else {
		reply, attachments, err = Session.Respond(ctx, msg.Content, msg.Images, msg.Files, nil)
	}

	e.activeTurnsMu.Lock()
	delete(e.activeTurns, msg.SessionKey)
	e.activeTurnsMu.Unlock()

	e.setSessionStatus(msg.SessionKey, "idle")
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The turn was cancelled by /esc or shutdown; do not send an error reply.
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Error("agent turn timed out", "session", msg.SessionKey, "error", err)
			_ = p.Reply(ctx, msg.ReplyCtx, "Processing timed out. Please retry or send /esc to cancel.")
			return
		}
		slog.Error("agent turn error", "session", msg.SessionKey, "error", err)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Processing failed: %v", err))
		return
	}

	slog.Info("agent turn finished", "session", msg.SessionKey, "reply_len", len(reply), "attachments", len(attachments))

	if _, streaming := p.(StreamReplyer); !streaming {
		if err := p.Reply(ctx, msg.ReplyCtx, reply); err != nil {
			slog.Error("failed to send reply", "session", msg.SessionKey, "error", err)
		}
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

func (e *Engine) ensureSessionForEntry(ctx context.Context, ent *sessionEntry, sessionKey string) error {
	e.sessionsMu.Lock()
	// If a session already exists for this entry, use it even if the entry was
	// marked closed by /new while the current message was being processed.
	if ent.session != nil {
		ent.lastActivity = time.Now()
		e.sessionsMu.Unlock()
		return nil
	}
	if ent.closed {
		// /new was called before a session was created; drop the message.
		e.sessionsMu.Unlock()
		return nil
	}
	agentName := ent.agent
	projectName := ent.project
	e.sessionsMu.Unlock()

	agent, ok := e.agents[agentName]
	if !ok {
		return fmt.Errorf("unknown agent %q", agentName)
	}
	project, ok := e.projects[projectName]
	if !ok {
		return fmt.Errorf("unknown project %q", projectName)
	}

	prevSessionID, err := e.getSessionID(sessionKey)
	if err != nil {
		return fmt.Errorf("load session id: %w", err)
	}
	s, err := agent.StartSession(ctx, sessionKey, project, prevSessionID)
	if err != nil {
		e.sessionsMu.Lock()
		ent.status = "error"
		e.sessionsMu.Unlock()
		return err
	}

	e.sessionsMu.Lock()
	if ent.closed || ent.session != nil {
		_ = s.Close()
	} else {
		ent.session = s
		ent.status = "idle"
		ent.lastActivity = time.Now()
		e.setSessionID(sessionKey, s.SessionID())
	}
	e.sessionsMu.Unlock()
	return nil
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
	case "help", "?":
		e.handleHelpCommand(ctx, p, msg)
	case "esc":
		e.handleEscCommand(ctx, p, msg)
	case "p":
		e.handlePCommand(ctx, p, msg)
	case "new":
		e.handleNewCommand(ctx, p, msg)
	default:
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown command: /%s", cmd.name))
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
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Current agent: %s\nAvailable: %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.agents[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown agent: %s", arg))
		return
	}
	e.setSessionAgent(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched agent to %s. Takes effect on the next message.", arg))
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
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Current project: %s\nAvailable: %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.projects[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown project: %s", arg))
		return
	}
	e.setSessionProject(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched project to %s. Takes effect on the next message.", arg))
}

func (e *Engine) handleHelpCommand(ctx context.Context, p Platform, msg *Message) {
	_ = p.Reply(ctx, msg.ReplyCtx, `Available commands:
/agent — show the current agent and available agents
/agent <name> — switch to the named agent
/proj — show the current project and available projects
/proj <name> — switch to the named project
/esc — cancel the currently generating reply
/p — show current agent, project, session status, and tool/model info
/new — start a new session (closes the current one, next message starts fresh)
/help, /? — show this help`)
}

func (e *Engine) handleEscCommand(ctx context.Context, p Platform, msg *Message) {
	e.activeTurnsMu.Lock()
	cancel, ok := e.activeTurns[msg.SessionKey]
	e.activeTurnsMu.Unlock()
	if ok {
		cancel()
		_ = p.Reply(ctx, msg.ReplyCtx, "Current reply cancelled.")
		return
	}
	_ = p.Reply(ctx, msg.ReplyCtx, "No reply is currently being generated.")
}

func (e *Engine) handlePCommand(ctx context.Context, p Platform, msg *Message) {
	sessionKey := msg.SessionKey
	agentName := e.sessionAgent(sessionKey)
	projectName := e.sessionProject(sessionKey)

	var lines []string
	lines = append(lines, fmt.Sprintf("Agent: %s", agentName))
	lines = append(lines, fmt.Sprintf("Project: %s", projectName))

	e.sessionsMu.Lock()
	ent, ok := e.sessions[sessionKey]
	e.sessionsMu.Unlock()

	if !ok || ent == nil || ent.session == nil {
		lines = append(lines, "Status: idle")
		_ = p.Reply(ctx, msg.ReplyCtx, strings.Join(lines, "\n"))
		return
	}

	st := ent.session.Status()

	// Model configuration.
	if st.Model != "" {
		lines = append(lines, fmt.Sprintf("Model: %s", st.Model))
	}
	if st.ReasoningEffort != "" {
		lines = append(lines, fmt.Sprintf("Reasoning effort: %s", st.ReasoningEffort))
	}

	// Session state.
	lines = append(lines, fmt.Sprintf("Status: %s", st.State))
	if st.TurnDuration > 0 {
		lines = append(lines, fmt.Sprintf("Elapsed: %s", formatDuration(st.TurnDuration)))
	}
	if st.ContextSize > 0 {
		lines = append(lines, fmt.Sprintf("Context: %s", formatContext(st.ContextUsed, st.ContextSize)))
	}

	// Active tool.
	if st.ToolCount > 0 {
		lines = append(lines, fmt.Sprintf("Tools used: %d", st.ToolCount))
		if st.CurrentToolDuration > 0 {
			lines = append(lines, fmt.Sprintf("Current tool: %s", formatDuration(st.CurrentToolDuration)))
		}
	}
	if st.CurrentToolCommand != "" {
		lines = append(lines, fmt.Sprintf("Command: %s", truncate(st.CurrentToolCommand, 120)))
	}

	// Token usage for this turn.
	if st.InputTokens > 0 || st.OutputTokens > 0 {
		lines = append(lines, fmt.Sprintf("Tokens: %d / %d", st.InputTokens, st.OutputTokens))
	}

	_ = p.Reply(ctx, msg.ReplyCtx, strings.Join(lines, "\n"))
}


func (e *Engine) handleNewCommand(ctx context.Context, p Platform, msg *Message) {
	sessionKey := msg.SessionKey

	e.sessionsMu.Lock()
	ent, ok := e.sessions[sessionKey]
	if ok && ent != nil {
		ent.closed = true
		ent.queue = nil
	}
	delete(e.sessions, sessionKey)
	e.sessionsMu.Unlock()
	e.deleteSessionID(sessionKey)

	if ok && ent != nil && ent.session != nil {
		if err := ent.session.Close(); err != nil {
			slog.Warn("new command: close session error", "session", sessionKey, "error", err)
		}
	}

	_ = p.Reply(ctx, msg.ReplyCtx, "New session created. The next message will start a fresh conversation.")
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Second).String()
}

func formatContext(used, size int) string {
	if size <= 0 {
		return ""
	}
	pct := float64(used) / float64(size) * 100
	return fmt.Sprintf("%.0f%% / %s", pct, formatSize(size))
}

func formatSize(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	default:
		return strconv.Itoa(n)
	}
}

func truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
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
	e.deleteSessionID(sessionKey)
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
