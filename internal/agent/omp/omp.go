// Package omp adapts the local `omp` agent to the core.Agent interface using
// the Agent Client Protocol (ACP) over stdio.
package omp

import (
	"context"
	"sync"

	"github.com/justmao945/omp-im/internal/acp"
	"github.com/justmao945/omp-im/internal/core"
)

// sessionRecord tracks an active ACP session and its project.
type sessionRecord struct {
	session *acp.Session
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
func (a *Agent) StartSession(ctx context.Context, sessionKey string, project core.Project, resumeSessionID string) (core.AgentSession, error) {
	cmd, args := resolveOMPCommand()
	cfg := acp.Config{Command: cmd, Args: args, WorkDir: project.WorkDir, AutoApproveTools: true}
	tr, err := acp.NewTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	s, err := acp.NewSession(ctx, cfg, sessionKey, resumeSessionID, tr)
	if err != nil {
		return nil, err
	}
	s.OnClose = func() { a.RemoveSession(sessionKey) }
	a.mu.Lock()
	a.sessions[sessionKey] = sessionRecord{session: s, project: project.Name}
	a.mu.Unlock()
	return s, nil
}

// resolveOMPCommand returns the command and arguments for spawning the omp ACP
// server. The caller is responsible for ensuring the executable is on PATH.
func resolveOMPCommand() (string, []string) {
	return "omp", []string{"acp"}
}

// RemoveSession drops a session from the agent registry, usually after Close.
func (a *Agent) RemoveSession(sessionKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionKey)
}

