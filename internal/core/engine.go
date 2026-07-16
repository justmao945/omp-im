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

	// lastListings caches the most recent /ls result per session key so /sw
	// can resolve an index argument.
	lastListings map[string][]SessionInfo

	activeTurns   map[string]context.CancelFunc
	activeTurnsMu sync.Mutex

	displayMode   string
	displayFooter bool
	displayMu     sync.RWMutex
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
		agents:         agents,
		defaultAgent:   defaultAgent,
		projects:       projects,
		defaultProject: defaultProject,
		platforms:      make([]Platform, 0),
		ctx:            ctx,
		cancel:         cancel,
		sessions:       make(map[string]*sessionEntry),
		lastListings:   make(map[string][]SessionInfo),
		activeTurns:    make(map[string]context.CancelFunc),
		displayFooter:  true,
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

func (e *Engine) AddPlatform(p Platform) {
	e.platforms = append(e.platforms, p)
	if s, ok := p.(DisplaySetter); ok {
		s.SetDisplayProvider(e)
	}
}

// DisplayMode returns the current stream display mode ("" or "full").
func (e *Engine) DisplayMode() string {
	e.displayMu.RLock()
	defer e.displayMu.RUnlock()
	return e.displayMode
}

// SetDisplayMode sets the stream display mode. Only "" and "full" are valid.
func (e *Engine) SetDisplayMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" && mode != "full" {
		return
	}
	e.displayMu.Lock()
	e.displayMode = mode
	e.displayMu.Unlock()
}

// DisplayFooter reports whether the turn-summary footer is appended.
func (e *Engine) DisplayFooter() bool {
	e.displayMu.RLock()
	defer e.displayMu.RUnlock()
	return e.displayFooter
}

// SetDisplayFooter enables or disables the turn-summary footer.
func (e *Engine) SetDisplayFooter(enabled bool) {
	e.displayMu.Lock()
	e.displayFooter = enabled
	e.displayMu.Unlock()
}

// Run starts all platforms and blocks until Stop is called.
func (e *Engine) Run() error {
	for _, p := range e.platforms {
		go func(p Platform) {
			if err := p.Start(e.handleMessage); err != nil {
				slog.Error("platform stopped", "platform", p.Name(), "error", err)
			}
		}(p)
	}

	<-e.ctx.Done()
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

	// // prefix: strip one leading slash and pass the remainder to the agent
	// as a normal prompt. This lets users invoke agent-side slash commands
	// (e.g. //web query → /web query) without omp-im intercepting them.
	if strings.HasPrefix(msg.Content, "//") {
		passthrough := *msg
		passthrough.Content = msg.Content[1:]
		e.queueNormalMessage(p, &passthrough)
		return
	}

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

// streamReplyer returns a platform's streamer when streaming is enabled. Platforms
// that do not expose StreamingEnabled retain the existing streaming behavior.
func streamReplyer(p Platform) (StreamReplyer, bool) {
	streamer, ok := p.(StreamReplyer)
	if !ok {
		return nil, false
	}
	if control, ok := p.(interface{ StreamingEnabled() bool }); ok && !control.StreamingEnabled() {
		return nil, false
	}
	return streamer, true
}

// buildNonStreamingFooter builds a turn footer for non-streaming replies from
// the session's final status and the recorded turn start time. It returns an
// empty string when the footer is disabled.
func buildNonStreamingFooter(footerEnabled bool, session AgentSession, turnStart time.Time) string {
	if !footerEnabled {
		return ""
	}
	st := session.Status()
	return BuildFooter(FooterInfo{
		Duration:    time.Since(turnStart),
		ContextUsed: st.ContextUsed,
		ContextSize: st.ContextSize,
		ToolCount:   st.ToolCount,
	})
}

func (e *Engine) processNormalMessage(ctx context.Context, cancel context.CancelFunc, p Platform, msg *Message, ent *sessionEntry) {
	streamer, streaming := streamReplyer(p)

	// Send an initial empty stream frame so the WeCom client creates a message
	// bubble with the "typing" animation before any content arrives. This must
	// happen before session setup so the user sees immediate feedback.
	if streaming {
		if err := streamer.StreamReply(ctx, msg.ReplyCtx, "", false); err != nil {
			slog.Error("failed to send initial stream frame", "session", msg.SessionKey, "error", err)
		}
		// Now send the processing event which sets the status line.
		if err := streamer.StreamEvent(ctx, msg.ReplyCtx, StreamEvent{Type: "processing"}); err != nil {
			slog.Error("failed to send initial stream event", "session", msg.SessionKey, "error", err)
		}
	}

	if err := e.ensureSessionForEntry(ctx, ent, msg.SessionKey); err != nil {
		slog.Error("failed to start session", "session", msg.SessionKey, "error", err)
		if streaming {
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer finishCancel()
			_ = streamer.StreamReply(finishCtx, msg.ReplyCtx, fmt.Sprintf("Failed to start session: %v", err), true)
		} else {
			_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Failed to start session: %v", err))
		}
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
	turnStart := time.Now()

	if streaming {
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
		if errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled) {
			// The turn was cancelled by /esc or shutdown. Finalize the
			// stream (partial text was already shown incrementally) and
			// return silently — the user already saw the /esc reply.
			if streaming {
				finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer finishCancel()
				if ferr := streamer.StreamReply(finishCtx, msg.ReplyCtx, "", true); ferr != nil {
					slog.Error("failed to finish stream reply on cancel", "session", msg.SessionKey, "error", ferr)
				}
			}
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Error("agent turn timed out", "session", msg.SessionKey, "error", err)
			if streaming {
				finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer finishCancel()
				_ = streamer.StreamReply(finishCtx, msg.ReplyCtx, "Processing timed out. Please retry or send /esc to cancel.", true)
			} else {
				_ = p.Reply(ctx, msg.ReplyCtx, "Processing timed out. Please retry or send /esc to cancel.")
			}
			return
		}
		slog.Error("agent turn error", "session", msg.SessionKey, "error", err)
		if streaming {
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer finishCancel()
			_ = streamer.StreamReply(finishCtx, msg.ReplyCtx, fmt.Sprintf("Processing failed: %v", err), true)
		} else {
			_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Processing failed: %v", err))
		}
		return
	}

	slog.Info("agent turn finished", "session", msg.SessionKey, "reply_len", len(reply), "attachments", len(attachments))

	if !streaming {
		if f := buildNonStreamingFooter(e.DisplayFooter(), Session, turnStart); f != "" {
			reply = strings.TrimSpace(reply) + "\n\n" + f
		}
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
		arg = strings.Join(parts[1:], " ")
	}
	return command{name: name, arg: arg}, true
}

func (e *Engine) handleCommand(ctx context.Context, p Platform, msg *Message, cmd command) {
	switch cmd.name {
	case "agent":
		e.handleAgentCommand(ctx, p, msg, cmd.arg)
	case "display":
		e.handleDisplayCommand(ctx, p, msg, cmd.arg)
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
	case "ls":
		e.handleLsCommand(ctx, p, msg)
	case "sw":
		e.handleSwCommand(ctx, p, msg, cmd.arg)
	default:
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown command: `/%s`", cmd.name))
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
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("## Agent\n\n**Current:** %s\n**Available:** %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.agents[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown agent: `%s`", arg))
		return
	}
	e.setSessionAgent(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched agent to **%s**. Takes effect on the next message.", arg))
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
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("## Project\n\n**Current:** %s\n**Available:** %s", current, strings.Join(names, ", ")))
		return
	}
	if _, ok := e.projects[arg]; !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Unknown project: `%s`", arg))
	}
	e.setSessionProject(sessionKey, arg)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched project to **%s**. Takes effect on the next message.", arg))
}

func (e *Engine) handleHelpCommand(ctx context.Context, p Platform, msg *Message) {
	_ = p.Reply(ctx, msg.ReplyCtx, "## Available Commands\n\n"+
		"**Agent & Project**\n"+
		"- `/agent` — show the current agent and available agents\n"+
		"- `/agent <name>` — switch to the named agent\n"+
		"- `/proj` — show the current project and available projects\n"+
		"- `/proj <name>` — switch to the named project\n"+
		"\n**Session**\n"+
		"- `/new` — start a new session (closes the current one, next message starts fresh)\n"+
		"- `/sw <n or id>` — switch to one of the listed sessions (resumes it next message)\n"+
		"\n**Status & Control**\n"+
		"- `/display` — show stream display settings; `/display mode full|simple` sets rendering, `/display footer on|off` toggles the turn-summary footer\n"+
		"- `/esc` — cancel the currently generating reply\n"+
		"\n**Other**\n"+
		"- `//<cmd>` — pass a slash command through to the agent (e.g. `//web query`)\n"+
		"- `/help`, `/?` — show this help")
}

func (e *Engine) handleDisplayCommand(ctx context.Context, p Platform, msg *Message, arg string) {
	arg = strings.ToLower(strings.TrimSpace(arg))

	// /display footer on|off
	if strings.HasPrefix(arg, "footer") {
		sub := strings.TrimSpace(strings.TrimPrefix(arg, "footer"))
		switch sub {
		case "on", "true", "1":
			e.SetDisplayFooter(true)
			_ = p.Reply(ctx, msg.ReplyCtx, "Display footer: **on**")
		case "off", "false", "0":
			e.SetDisplayFooter(false)
			_ = p.Reply(ctx, msg.ReplyCtx, "Display footer: **off**")
		default:
			_ = p.Reply(ctx, msg.ReplyCtx, "Usage: `/display footer on` or `/display footer off`.")
		}
		return
	}

	// /display mode full|simple
	if strings.HasPrefix(arg, "mode") {
		sub := strings.TrimSpace(strings.TrimPrefix(arg, "mode"))
		switch sub {
		case "full":
			e.SetDisplayMode("full")
			_ = p.Reply(ctx, msg.ReplyCtx, "Display mode: **full** (thinking + tools)")
		case "simple", "simplified", "off", "concise":
			e.SetDisplayMode("")
			_ = p.Reply(ctx, msg.ReplyCtx, "Display mode: **simplified** (body only)")
		default:
			_ = p.Reply(ctx, msg.ReplyCtx, "Usage: `/display mode full` or `/display mode simple`.")
		}
		return
	}

	// /display — show current settings.
	switch arg {
	case "", "status":
		_ = p.Reply(ctx, msg.ReplyCtx, e.displayStatus())
	default:
		_ = p.Reply(ctx, msg.ReplyCtx, "Usage:\n"+
			"- `/display` — show current settings\n"+
			"- `/display mode full` | `/display mode simple` — stream rendering\n"+
			"- `/display footer on` | `/display footer off` — turn-summary footer")
	}
}

// displayStatus renders the current display settings as a status message.
func (e *Engine) displayStatus() string {
	modeLabel := "simplified (body only)"
	if e.DisplayMode() == "full" {
		modeLabel = "full (thinking + tools)"
	}
	footerState := "off"
	if e.DisplayFooter() {
		footerState = "on"
	}
	return fmt.Sprintf("## Display\n\n- **Mode:** %s\n- **Footer:** %s", modeLabel, footerState)
}

func (e *Engine) handleEscCommand(ctx context.Context, p Platform, msg *Message) {
	sessionKey := msg.SessionKey

	e.activeTurnsMu.Lock()
	_, hasActive := e.activeTurns[sessionKey]
	e.activeTurnsMu.Unlock()
	if !hasActive {
		_ = p.Reply(ctx, msg.ReplyCtx, "No reply is currently being generated.")
		return
	}

	// Send session/cancel to the agent so it stops generating and wasting
	// tokens. The agent will respond to the in-flight session/prompt with
	// stopReason "cancelled", which Respond surfaces as core.ErrCancelled.
	e.sessionsMu.Lock()
	ent, ok := e.sessions[sessionKey]
	e.sessionsMu.Unlock()
	if ok && ent != nil && ent.session != nil {
		if err := ent.session.Cancel(); err != nil {
			slog.Warn("esc: session/cancel failed, falling back to context cancel", "session", sessionKey, "error", err)
			e.activeTurnsMu.Lock()
			cancel, ok := e.activeTurns[sessionKey]
			e.activeTurnsMu.Unlock()
			if ok {
				cancel()
			}
		}
	}
	_ = p.Reply(ctx, msg.ReplyCtx, "Current reply cancelled.")
}

func (e *Engine) handlePCommand(ctx context.Context, p Platform, msg *Message) {
	sessionKey := msg.SessionKey
	agentName := e.sessionAgent(sessionKey)
	projectName := e.sessionProject(sessionKey)

	var lines []string
	lines = append(lines, "## Status")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("- **Agent:** %s", agentName))
	lines = append(lines, fmt.Sprintf("- **Project:** %s", projectName))
	if proj, ok := e.projects[projectName]; ok && proj.WorkDir != "" {
		lines = append(lines, fmt.Sprintf("- **Path:** `%s`", proj.WorkDir))
	}

	e.sessionsMu.Lock()
	ent, ok := e.sessions[sessionKey]
	e.sessionsMu.Unlock()

	if !ok || ent == nil || ent.session == nil {
		_ = p.Reply(ctx, msg.ReplyCtx, strings.Join(lines, "\n"))
		return
	}

	st := ent.session.Status()

	// Model configuration.
	if st.Model != "" {
		lines = append(lines, fmt.Sprintf("- **Model:** %s", st.Model))
	}
	if st.ReasoningEffort != "" {
		lines = append(lines, fmt.Sprintf("- **Reasoning effort:** %s", st.ReasoningEffort))
	}

	// Context usage.
	if st.ContextSize > 0 {
		lines = append(lines, fmt.Sprintf("- **Context:** %s", formatContext(st.ContextUsed, st.ContextSize)))
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
func (e *Engine) handleLsCommand(ctx context.Context, p Platform, msg *Message) {
	sessionKey := msg.SessionKey
	agentName := e.sessionAgent(sessionKey)
	projectName := e.sessionProject(sessionKey)
	proj, ok := e.projects[projectName]
	if !ok || proj.WorkDir == "" {
		_ = p.Reply(ctx, msg.ReplyCtx, "No working directory for the current project.")
		return
	}
	lister, ok := e.agents[agentName].(SessionLister)
	if !ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Agent `%s` does not support listing historical sessions.", agentName))
		return
	}
	sessions, err := lister.ListSessions(ctx, proj.WorkDir, 20)
	if err != nil {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Failed to list sessions: %v", err))
		return
	}
	e.sessionsMu.Lock()
	e.lastListings[sessionKey] = sessions
	e.sessionsMu.Unlock()
	if len(sessions) == 0 {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("No historical sessions for **%s** in `%s`.", agentName, proj.WorkDir))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Sessions — %s @ %s\n\n", agentName, projectName)
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(&b, "%d. `%s` · %s · %s\n", i+1, shortID(s.ID), title, formatRelativeTime(s.UpdatedAt))
	}
	b.WriteString("\n`/sw <n or id>` resumes one of the sessions above.")
	_ = p.Reply(ctx, msg.ReplyCtx, b.String())
}

func (e *Engine) handleSwCommand(ctx context.Context, p Platform, msg *Message, arg string) {
	sessionKey := msg.SessionKey
	if arg == "" {
		_ = p.Reply(ctx, msg.ReplyCtx, "Usage: `/sw <n or session id>` — list sessions with `/ls`.")
		return
	}
	e.sessionsMu.Lock()
	listing := append([]SessionInfo(nil), e.lastListings[sessionKey]...)
	e.sessionsMu.Unlock()

	var targetID string
	if n, err := strconv.Atoi(arg); err == nil {
		idx := n - 1
		if idx < 0 || idx >= len(listing) {
			_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("No session #%s. Run `/ls` to list sessions.", arg))
			return
		}
		targetID = listing[idx].ID
	} else {
		for _, s := range listing {
			if strings.HasPrefix(s.ID, arg) {
				targetID = s.ID
				break
			}
		}
		if targetID == "" {
			_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("No session matching `%s`. Run `/ls` first.", arg))
			return
		}
	}

	e.switchSessionID(sessionKey, targetID)
	_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched to session `%s`. The next message resumes that conversation.", shortID(targetID)))
}

// switchSessionID closes any live ACP session for sessionKey and persists
// targetID as the resume target so the next message resumes that conversation.
// The current agent and project selection are preserved.
func (e *Engine) switchSessionID(sessionKey, sessionID string) {
	e.sessionsMu.Lock()
	if ent, ok := e.sessions[sessionKey]; ok && ent != nil {
		if ent.session != nil {
			_ = ent.session.Close()
		}
		ent.session = nil
		ent.closed = false
		ent.queue = nil
		ent.status = "idle"
		ent.lastActivity = time.Now()
	}
	e.sessionsMu.Unlock()
	e.setSessionID(sessionKey, sessionID)
}

// shortID returns the first 8 characters of a session id for display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// formatRelativeTime renders a time as a human-friendly relative duration.
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.UTC().Format("2006-01-02")
	}
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
