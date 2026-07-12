// Package omp adapts the local `omp` agent to the core.Agent interface using
// the Agent Client Protocol (ACP) over stdio.
package omp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	cmd, args := resolveOMPCommand()
	cfg := agentConfig{Command: cmd, Args: args, WorkDir: project.WorkDir, AutoApproveTools: true}
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

// resolveOMPCommand returns the command and arguments for spawning the omp ACP
// server. When omp resolves to a script with a bun shebang, it invokes bun
// directly using the bun binary found in PATH or in the same directory as omp.
// This avoids "no such file or directory" failures when the process manager
// that starts omp-im provides a PATH that does not include bun.
func resolveOMPCommand() (string, []string) {
	command := "omp"
	args := []string{"acp"}

	ompPath, err := exec.LookPath(command)
	if err != nil {
		return command, args
	}

	shebang, err := readShebang(ompPath)
	if err != nil || !shebangUsesBun(shebang) {
		return command, args
	}

	bunPath, err := findBun(ompPath)
	if err != nil {
		return command, args
	}

	return bunPath, append(append(shebangBunArgs(shebang), ompPath), args...)
}

func readShebang(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#!") {
		return "", nil
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "#!")), nil
}

func shebangUsesBun(shebang string) bool {
	return filepath.Base(shebangProgram(shebang)) == "bun"
}

func shebangProgram(shebang string) string {
	fields := strings.Fields(shebang)
	if len(fields) == 0 {
		return ""
	}
	i := 0
	if strings.HasSuffix(fields[0], "/env") {
		i = 1
		if i < len(fields) && fields[i] == "-S" {
			i++
		}
	}
	if i < len(fields) {
		return fields[i]
	}
	return ""
}

func shebangBunArgs(shebang string) []string {
	fields := strings.Fields(shebang)
	if len(fields) == 0 {
		return nil
	}
	i := 0
	if strings.HasSuffix(fields[0], "/env") {
		i = 1
		if i < len(fields) && fields[i] == "-S" {
			i++
		}
	}
	i++ // skip the bun program itself
	if i < len(fields) {
		return fields[i:]
	}
	return nil
}

func findBun(ompPath string) (string, error) {
	if p, err := exec.LookPath("bun"); err == nil {
		return p, nil
	}
	candidate := filepath.Join(filepath.Dir(ompPath), "bun")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", errors.New("bun not found")
}

// RemoveSession drops a session from the agent registry, usually after Close.
func (a *Agent) RemoveSession(sessionKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionKey)
}
