package agent

import (
	"context"
	"sync"

	"github.com/justmao945/omp-im/internal/acp"
	"github.com/justmao945/omp-im/internal/core"
)

// localACPConfig identifies a locally installed ACP agent command.
type localACPConfig struct {
	name             string
	command          string
	args             []string
	authMethod       string
	autoApproveTools bool
	installHint      string
}

type localACPSessionRecord struct {
	session *acp.Session
	project string
}

// localACPAgent starts one local ACP process for each IM conversation.
type localACPAgent struct {
	cfg      localACPConfig
	sessions map[string]localACPSessionRecord
	mu       sync.Mutex
}

func newLocalACPAgent(cfg localACPConfig) *localACPAgent {
	cfg.args = append([]string(nil), cfg.args...)
	return &localACPAgent{cfg: cfg, sessions: make(map[string]localACPSessionRecord)}
}

func (a *localACPAgent) Name() string { return a.cfg.name }

// Stop is a no-op; sessions own their transports.
func (a *localACPAgent) Stop() error { return nil }

func (a *localACPAgent) StartSession(ctx context.Context, sessionKey string, project core.Project, resumeSessionID string) (core.AgentSession, error) {
	cfg := acp.Config{
		Command:          a.cfg.command,
		Args:             a.cfg.args,
		WorkDir:          project.WorkDir,
		AutoApproveTools: a.cfg.autoApproveTools,
		AuthMethod:       a.cfg.authMethod,
		InstallHint:      a.cfg.installHint,
	}
	tr, err := acp.NewTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	s, err := acp.NewSession(ctx, cfg, sessionKey, resumeSessionID, tr)
	if err != nil {
		_ = tr.Close()
		return nil, err
	}
	s.OnClose = func() { a.removeSession(sessionKey) }
	a.mu.Lock()
	a.sessions[sessionKey] = localACPSessionRecord{session: s, project: project.Name}
	a.mu.Unlock()
	return s, nil
}

func (a *localACPAgent) removeSession(sessionKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionKey)
}
