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
func (a *Agent) Name() string { return "omp" }

// Stop is a no-op; sessions own their transports.
func (a *Agent) Stop() error { return nil }

// StartSession creates a new ACP conversation session.
func (a *Agent) StartSession(ctx context.Context, sessionKey string) (core.AgentSession, error) {
	tr, err := newTransport(a.cfg, nil)
	if err != nil {
		return nil, err
	}
	return newACPSession(ctx, a.cfg, sessionKey, tr)
}
