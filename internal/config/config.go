// Package config parses omp-im's JSON configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SupportedAgentNames lists agent names that have built-in implementations.
var SupportedAgentNames = []string{"omp"}

// Config is the top-level configuration.
type Config struct {
	Agents       []string        `json:"agents"`
	Projects     []ProjectConfig `json:"projects"`
	Defaults     DefaultsConfig  `json:"default"`
	Platforms    []PlatformConfig `json:"platforms"`
	// SessionStore is the path to a JSON file that persists agent session IDs
	// across restarts. If empty, it defaults to <user home>/.omp-im/sessions.json.
	SessionStore string `json:"session_store,omitempty"`
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

// DefaultPath returns the default configuration path (~/.omp-im/config.json).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".omp-im", "config.json")
	}
	return filepath.Join(home, ".omp-im", "config.json")
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
		c.Agents = []string{"omp"}
	}
	if len(c.Projects) == 0 {
		c.Projects = []ProjectConfig{{Name: "default", WorkDir: ""}}
	}
	if c.Defaults.Agent == "" {
		c.Defaults.Agent = c.Agents[0]
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
	known := make(map[string]struct{}, len(SupportedAgentNames))
	for _, n := range SupportedAgentNames {
		known[n] = struct{}{}
	}
	agentSet := make(map[string]struct{}, len(c.Agents))
	for i, name := range c.Agents {
		if name == "" {
			return fmt.Errorf("agents[%d] is empty", i)
		}
		if _, ok := known[name]; !ok {
			return fmt.Errorf("agents[%d] %q is not supported (supported: %v)", i, name, SupportedAgentNames)
		}
		if _, ok := agentSet[name]; ok {
			return fmt.Errorf("duplicate agent name %q", name)
		}
		agentSet[name] = struct{}{}
	}
	if _, ok := agentSet[c.Defaults.Agent]; !ok {
		return fmt.Errorf("default.agent %q not found in agents", c.Defaults.Agent)
	}

	if len(c.Projects) == 0 {
		return fmt.Errorf("at least one project is required")
	}
	projectSet := make(map[string]struct{}, len(c.Projects))
	for i, p := range c.Projects {
		if p.Name == "" {
			return fmt.Errorf("projects[%d].name is required", i)
		}
		if _, ok := projectSet[p.Name]; ok {
			return fmt.Errorf("duplicate project name %q", p.Name)
		}
		projectSet[p.Name] = struct{}{}
	}
	if _, ok := projectSet[c.Defaults.Project]; !ok {
		return fmt.Errorf("default.project %q not found in projects", c.Defaults.Project)
	}

	if len(c.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required")
	}
	for i, p := range c.Platforms {
		switch p.Type {
		case "weixin", "http":
			// ok
		default:
			return fmt.Errorf("platforms[%d] unsupported type %q", i, p.Type)
		}
	}
	return nil
}

// SessionStorePath returns the effective path for persisting session IDs.
func (c *Config) SessionStorePath() string {
	if c.SessionStore != "" {
		return c.SessionStore
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".omp-im", "sessions.json")
	}
	return filepath.Join(home, ".omp-im", "sessions.json")
}
func (c *Config) Project(name string) (ProjectConfig, bool) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return ProjectConfig{}, false
}
