package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidACPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
"agent": {
  "command": "omp",
  "args": ["acp"],
  "work_dir": "/tmp"
},
"platforms": [
  {
    "type": "weixin",
    "options": {
      "token": "ilink-token",
      "allow_from": "user@im.wechat"
    }
  }
]
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Agent.Command != "omp" {
		t.Fatalf("command = %q", cfg.Agent.Command)
	}
	if len(cfg.Agent.Args) != 1 || cfg.Agent.Args[0] != "acp" {
		t.Fatalf("args = %v", cfg.Agent.Args)
	}
	if len(cfg.Platforms) != 1 {
		t.Fatalf("platforms = %d", len(cfg.Platforms))
	}
}

func TestDefaults(t *testing.T) {
	cfg := &Config{
		Agent:     AgentConfig{},
		Platforms: []PlatformConfig{{Type: "weixin", Options: map[string]any{"token": "x"}}},
	}
	cfg.applyDefaults()
	if cfg.Agent.Command != "omp" {
		t.Fatalf("default command = %q", cfg.Agent.Command)
	}
	if len(cfg.Agent.Args) != 1 || cfg.Agent.Args[0] != "acp" {
		t.Fatalf("default args = %v", cfg.Agent.Args)
	}
}

func TestValidateRequiresToken(t *testing.T) {
	cfg := &Config{
		Agent:     AgentConfig{Command: "omp", Args: []string{"acp"}},
		Platforms: []PlatformConfig{{Type: "weixin", Options: map[string]any{}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestValidateRequiresCommand(t *testing.T) {
	cfg := &Config{
		Agent:     AgentConfig{},
		Platforms: []PlatformConfig{{Type: "weixin", Options: map[string]any{"token": "x"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing command")
	}
}
