package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, data string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	data := `{
"agents": [
  {"name": "omp", "type": "omp", "command": "omp", "args": ["acp"]}
],
"projects": [
  {"name": "p1", "work_dir": "/tmp/p1"}
],
"default": {"agent": "omp", "project": "p1"},
"platforms": [{"type": "weixin", "options": {}}]
}`
	cfg, err := Load(writeConfig(t, t.TempDir(), data))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "omp" {
		t.Fatalf("agents = %+v", cfg.Agents)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "p1" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
	if cfg.Defaults.Agent != "omp" || cfg.Defaults.Project != "p1" {
		t.Fatalf("defaults = %+v", cfg.Defaults)
	}
}

func TestDefaultsFillEmpty(t *testing.T) {
	data := `{"platforms": [{"type": "weixin", "options": {}}]}`
	cfg, err := Load(writeConfig(t, t.TempDir(), data))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "omp" {
		t.Fatalf("agents = %+v", cfg.Agents)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "default" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
	if cfg.Defaults.Agent != "omp" || cfg.Defaults.Project != "default" {
		t.Fatalf("defaults = %+v", cfg.Defaults)
	}
}

func TestLegacyAgentWorkDirBecomesProject(t *testing.T) {
	data := `{
"agents": [{"name": "omp", "type": "omp", "command": "omp", "args": ["acp"], "work_dir": "/tmp/legacy"}],
"platforms": [{"type": "weixin", "options": {}}]
}`
	cfg, err := Load(writeConfig(t, t.TempDir(), data))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].WorkDir != "/tmp/legacy" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
}

func TestValidateRequiresAgentCommand(t *testing.T) {
	cfg := &Config{
		Agents:    []AgentConfig{{Name: "omp", Type: "omp"}},
		Projects:  []ProjectConfig{{Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestValidateDefaultAgentMustExist(t *testing.T) {
	cfg := &Config{
		Agents:    []AgentConfig{{Name: "omp", Type: "omp", Command: "omp"}},
		Projects:  []ProjectConfig{{Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "missing", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing default agent")
	}
}

func TestValidateDuplicateProjectName(t *testing.T) {
	cfg := &Config{
		Agents:    []AgentConfig{{Name: "omp", Type: "omp", Command: "omp"}},
		Projects:  []ProjectConfig{{Name: "p1"}, {Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate project name")
	}
}

func TestAgentAndProjectLookup(t *testing.T) {
	cfg := &Config{
		Agents:   []AgentConfig{{Name: "omp", Type: "omp", Command: "omp"}},
		Projects: []ProjectConfig{{Name: "p1", WorkDir: "/tmp"}},
	}
	if a, ok := cfg.Agent("omp"); !ok || a.Name != "omp" {
		t.Fatal("agent lookup failed")
	}
	if _, ok := cfg.Agent("missing"); ok {
		t.Fatal("expected missing agent lookup to fail")
	}
	if p, ok := cfg.Project("p1"); !ok || p.WorkDir != "/tmp" {
		t.Fatal("project lookup failed")
	}
}
