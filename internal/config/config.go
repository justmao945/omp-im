// Package config parses omp-im's JSON configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level configuration.
type Config struct {
	Agents    []AgentConfig    `json:"agents"`
	Projects  []ProjectConfig  `json:"projects"`
	Defaults  DefaultsConfig   `json:"default"`
	Platforms []PlatformConfig `json:"platforms"`
}

// AgentConfig configures an agent backend (omp, claude, codex, etc.).
type AgentConfig struct {
	// Name is the user-visible identifier used in /agent commands.
	Name string `json:"name"`
	// Type selects the adapter implementation.
	Type string `json:"type"`
	// Command is the agent binary.
	Command string `json:"command"`
	// Args are fixed arguments passed to the command.
	Args []string `json:"args"`
	// WorkDir is an optional global default working directory.
	WorkDir string `json:"work_dir"`
	// AutoApproveTools automatically approves all tool permission requests.
	AutoApproveTools bool `json:"auto_approve_tools"`
}

// ProjectConfig configures a project with its own working directory.
type ProjectConfig struct {
	Name    string `json:"name"`
	WorkDir string `json:"work_dir"`
}

// DefaultsConfig selects the agent and project for new sessions.
type DefaultsConfig struct {
	Agent   string `json:"agent"`
	Project string `json:"project"`
}

// PlatformConfig configures a single IM platform.
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
	if len(c.Agents) == 0 {
		c.Agents = []AgentConfig{{Name: "omp", Type: "omp", Command: "omp", Args: []string{"acp"}}}
	}
	for i := range c.Agents {
		if c.Agents[i].Name == "" {
			c.Agents[i].Name = c.Agents[i].Type
		}
		if c.Agents[i].Command == "" {
			c.Agents[i].Command = c.Agents[i].Type
		}
		if c.Agents[i].Type == "" {
			c.Agents[i].Type = c.Agents[i].Name
		}
	}
	if len(c.Projects) == 0 {
		if c.Agents[0].WorkDir != "" {
			c.Projects = []ProjectConfig{{Name: "default", WorkDir: c.Agents[0].WorkDir}}
		} else {
			c.Projects = []ProjectConfig{{Name: "default", WorkDir: ""}}
		}
	}
	if c.Defaults.Agent == "" {
		c.Defaults.Agent = c.Agents[0].Name
	}
	if c.Defaults.Project == "" {
		c.Defaults.Project = c.Projects[0].Name
	}
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	if len(c.Agents) == 0 {
		return fmt.Errorf("at least one agent is required")
	}
	agentNames := make(map[string]struct{}, len(c.Agents))
	for i, a := range c.Agents {
		if a.Name == "" {
			return fmt.Errorf("agents[%d].name is required", i)
		}
		if a.Type == "" {
			return fmt.Errorf("agents[%d].type is required", i)
		}
		if a.Command == "" {
			return fmt.Errorf("agents[%d].command is required", i)
		}
		if _, ok := agentNames[a.Name]; ok {
			return fmt.Errorf("duplicate agent name %q", a.Name)
		}
		agentNames[a.Name] = struct{}{}
	}
	if _, ok := agentNames[c.Defaults.Agent]; !ok {
		return fmt.Errorf("default.agent %q not found in agents", c.Defaults.Agent)
	}

	if len(c.Projects) == 0 {
		return fmt.Errorf("at least one project is required")
	}
	projectNames := make(map[string]struct{}, len(c.Projects))
	for i, p := range c.Projects {
		if p.Name == "" {
			return fmt.Errorf("projects[%d].name is required", i)
		}
		if _, ok := projectNames[p.Name]; ok {
			return fmt.Errorf("duplicate project name %q", p.Name)
		}
		projectNames[p.Name] = struct{}{}
	}
	if _, ok := projectNames[c.Defaults.Project]; !ok {
		return fmt.Errorf("default.project %q not found in projects", c.Defaults.Project)
	}

	if len(c.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required")
	}
	for i, p := range c.Platforms {
		switch p.Type {
		case "weixin":
			// Token is optional with QR login; nothing required here.
		default:
			return fmt.Errorf("platforms[%d] unsupported type %q", i, p.Type)
		}
	}
	return nil
}

// Agent returns the agent config with the given name, or false if missing.
func (c *Config) Agent(name string) (AgentConfig, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return AgentConfig{}, false
}

// Project returns the project config with the given name, or false if missing.
func (c *Config) Project(name string) (ProjectConfig, bool) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return ProjectConfig{}, false
}
