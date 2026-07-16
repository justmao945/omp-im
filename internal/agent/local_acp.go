package agent

import (
	"context"
	"sync"

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
	session *Session
	project string
}

// localACPAgent starts one local ACP process for each IM conversation.
type localACPAgent struct {
	cfg      localACPConfig
	mcpProxy *mcpWarmProxy
	sessions map[string]localACPSessionRecord
	mu       sync.Mutex
}

func newLocalACPAgent(cfg localACPConfig) *localACPAgent {
	cfg.args = append([]string(nil), cfg.args...)
	return &localACPAgent{
		cfg:      cfg,
		mcpProxy: newMCPWarmProxy(),
		sessions: make(map[string]localACPSessionRecord),
	}
}

func (a *localACPAgent) Name() string { return a.cfg.name }

func (a *localACPAgent) Stop() error {
	return a.mcpProxy.Close()
}

func (a *localACPAgent) StartSession(ctx context.Context, sessionKey string, project core.Project, resumeSessionID string) (core.AgentSession, error) {
	mcpServers, err := loadMCPServers(a.cfg.name, project.WorkDir)
	if err != nil {
		return nil, err
	}
	mcpServers, err = a.mcpProxy.warmHTTPServers(ctx, mcpServers)
	if err != nil {
		return nil, err
	}
	cfg := Config{
		Command:          a.cfg.command,
		Args:             a.cfg.args,
		WorkDir:          project.WorkDir,
		AutoApproveTools: a.cfg.autoApproveTools,
		MCPServers:       mcpServers,
		AuthMethod:       a.cfg.authMethod,
		InstallHint:      a.cfg.installHint,
	}
	tr, err := NewTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	s, err := NewSession(ctx, cfg, sessionKey, resumeSessionID, tr)
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
