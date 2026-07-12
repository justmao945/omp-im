// Package omp adapts the local `omp` agent to the core.Agent interface using
// the Agent Client Protocol (ACP) over stdio.
package omp

import (
	"context"
	"sync"

	"github.com/justmao945/omp-im/internal/core"
)

// agentConfig carries the runtime parameters for spawning an omp process.
type agentConfig struct {
	Command          string
	Args             []string
	WorkDir          string
	AutoApproveTools bool
}

type sessionRecord struct {
	session *acpSession
	project string
}

// Agent implements core.Agent for the omp agent.
type Agent struct {
	sessions map[string]sessionRecord
	mu       sync.Mutex
}

// New creates an omp agent adapter with built-in defaults.
func New() *Agent {
	return &Agent{sessions: make(map[string]sessionRecord)}
}

// Name returns the agent name.
func (a *Agent) Name() string { return "omp" }

// Stop is a no-op; sessions own their transports.
func (a *Agent) Stop() error { return nil }

// StartSession creates a new ACP conversation session in the given project.
func (a *Agent) StartSession(ctx context.Context, sessionKey string, project core.Project) (core.AgentSession, error) {
	cfg := agentConfig{Command: "omp", Args: []string{"acp"}, WorkDir: project.WorkDir, AutoApproveTools: true}
	tr, err := newTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	s, err := newACPSession(ctx, cfg, sessionKey, tr)
	if err != nil {
		return nil, err
	}
	s.onClose = func() { a.RemoveSession(sessionKey) }
	a.mu.Lock()
	a.sessions[sessionKey] = sessionRecord{session: s, project: project.Name}
	a.mu.Unlock()
	return s, nil
}

// ListSessions returns the active sessions managed by this agent.
func (a *Agent) ListSessions(ctx context.Context) ([]core.SessionInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	infos := make([]core.SessionInfo, 0, len(a.sessions))
	for key, rec := range a.sessions {
		infos = append(infos, core.SessionInfo{
			SessionKey:   key,
			Project:      rec.project,
			Status:       rec.session.status(),
			LastActivity: rec.session.lastActivity(),
		})
	}
	return infos, nil
}

// RemoveSession drops a session from the agent registry, usually after Close.
func (a *Agent) RemoveSession(sessionKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionKey)
}
