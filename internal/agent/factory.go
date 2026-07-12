// Package agent provides a factory for built-in agent backends.
package agent

import (
	"fmt"

	"github.com/justmao945/omp-im/internal/agent/omp"
	"github.com/justmao945/omp-im/internal/core"
)

// New creates a built-in agent by name.
func New(name string) (core.Agent, error) {
	switch name {
	case "omp":
		return omp.New(), nil
	case "claude", "codex":
		return nil, fmt.Errorf("agent %q is not yet implemented", name)
	default:
		return nil, fmt.Errorf("unknown agent %q", name)
	}
}
