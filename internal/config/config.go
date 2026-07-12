// Package config parses omp-im's JSON configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level configuration.
type Config struct {
	Agent     AgentConfig      `json:"agent"`
	Platforms []PlatformConfig `json:"platforms"`
}

// AgentConfig configures the omp agent ACP backend.
type AgentConfig struct {
	// Command is the ACP agent binary (default: "omp").
	Command string   `json:"command"`
	// Args are fixed arguments passed to the command (default: ["acp"]).
	Args []string `json:"args"`
	// WorkDir is the working directory for new ACP sessions.
	WorkDir string `json:"work_dir"`
	// AutoApproveTools automatically approves all tool permission requests.
	AutoApproveTools bool `json:"auto_approve_tools"`
}

// PlatformConfig configures a single platform.
type PlatformConfig struct {
	Type    string         `json:"type"`
	Options map[string]any `json:"options"`
}

// Load reads and parses the JSON file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Agent.Command == "" {
		c.Agent.Command = "omp"
	}
	if len(c.Agent.Args) == 0 {
		c.Agent.Args = []string{"acp"}
	}
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	if c.Agent.Command == "" {
		return fmt.Errorf("agent.command is required")
	}
	if len(c.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required")
	}

	for i, p := range c.Platforms {
		switch p.Type {
		case "weixin":
			if _, ok := p.Options["token"]; !ok {
				return fmt.Errorf("platforms[%d] weixin requires options.token", i)
			}
		default:
			return fmt.Errorf("platforms[%d] unsupported type %q", i, p.Type)
		}
	}
	return nil
}
