// Package omp adapts the local `omp` agent to the core.Agent interface using
// the Agent Client Protocol (ACP) over stdio.
package omp

import (
	"context"

	"github.com/justmao945/omp-im/internal/config"
	"github.com/justmao945/omp-im/internal/core"
)

// Agent implements core.Agent for the omp agent.
type Agent struct {
	cfg config.AgentConfig
}

// New creates an omp agent adapter from configuration.
func New(cfg config.AgentConfig) *Agent {
	return &Agent{cfg: cfg}
}

// Name returns the agent name.
func (a *Agent) Name() string { return a.cfg.Name }

// Stop is a no-op; sessions own their transports.
func (a *Agent) Stop() error { return nil }

// StartSession creates a new ACP conversation session in the given project.
func (a *Agent) StartSession(ctx context.Context, sessionKey string, project core.Project) (core.AgentSession, error) {
	cfg := a.cfg
	cfg.WorkDir = project.WorkDir
	tr, err := newTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	return newACPSession(ctx, cfg, sessionKey, tr)
}
