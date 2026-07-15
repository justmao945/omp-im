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
"agents": ["omp"],
"projects": [{"name": "p1", "work_dir": "/tmp/p1"}],
"default": {"agent": "omp", "project": "p1"},
"platforms": [{"type": "weixin", "options": {}}]
}`
	cfg, err := Load(writeConfig(t, t.TempDir(), data))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "omp" {
		t.Fatalf("agents = %v", cfg.Agents)
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
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "omp" {
		t.Fatalf("agents = %v", cfg.Agents)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "default" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
	if cfg.Defaults.Agent != "omp" || cfg.Defaults.Project != "default" {
		t.Fatalf("defaults = %+v", cfg.Defaults)
	}
}

func TestValidateUnsupportedAgent(t *testing.T) {
	cfg := &Config{
		Agents:    []string{"unsupported"},
		Projects:  []ProjectConfig{{Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "unsupported", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unsupported agent")
	}
}

func TestValidateClaudeAndCodexAgents(t *testing.T) {
	cfg := &Config{
		Agents:    []string{"claude", "codex"},
		Projects:  []ProjectConfig{{Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "codex", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}

func TestValidateDefaultAgentMustExist(t *testing.T) {
	cfg := &Config{
		Agents:    []string{"omp"},
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
		Agents:    []string{"omp"},
		Projects:  []ProjectConfig{{Name: "p1"}, {Name: "p1"}},
		Defaults:  DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{{Type: "weixin"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate project name")
	}
}

func TestProjectLookup(t *testing.T) {
	cfg := &Config{
		Agents:   []string{"omp"},
		Projects: []ProjectConfig{{Name: "p1", WorkDir: "/tmp"}},
	}
	if p, ok := cfg.Project("p1"); !ok || p.WorkDir != "/tmp" {
		t.Fatal("project lookup failed")
	}
	if _, ok := cfg.Project("missing"); ok {
		t.Fatal("expected missing project lookup to fail")
	}
}

func TestValidateDisplayMode(t *testing.T) {
	base := func() *Config {
		return &Config{
			Agents:    []string{"omp"},
			Projects:  []ProjectConfig{{Name: "p1"}},
			Defaults:  DefaultsConfig{Agent: "omp", Project: "p1"},
			Platforms: []PlatformConfig{{Type: "weixin"}},
		}
	}
	t.Run("empty is valid", func(t *testing.T) {
		cfg := base()
		cfg.Display = ""
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(): %v", err)
		}
		if cfg.Display != "" {
			t.Fatalf("Display = %q, want empty", cfg.Display)
		}
	})
	t.Run("full is valid", func(t *testing.T) {
		cfg := base()
		cfg.Display = "full"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(): %v", err)
		}
		if cfg.Display != "full" {
			t.Fatalf("Display = %q, want full", cfg.Display)
		}
	})
	t.Run("invalid rejected", func(t *testing.T) {
		cfg := base()
		cfg.Display = "verbose"
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for invalid display mode")
		}
	})
	t.Run("normalized to lowercase", func(t *testing.T) {
		cfg := base()
		cfg.Display = "FULL"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(): %v", err)
		}
		if cfg.Display != "full" {
			t.Fatalf("Display = %q, want full", cfg.Display)
		}
	})
}

func TestPlatformConfigWeixinAccount(t *testing.T) {
	cases := []struct {
		name string
		pc   PlatformConfig
		want string
	}{
		{"name only", PlatformConfig{Name: "work", Type: "weixin", Options: map[string]any{}}, "work"},
		{"name overrides account_id", PlatformConfig{Name: "work", Type: "weixin", Options: map[string]any{"account_id": "personal"}}, "work"},
		{"account_id fallback", PlatformConfig{Type: "weixin", Options: map[string]any{"account_id": "personal"}}, "personal"},
		{"default", PlatformConfig{Type: "weixin", Options: map[string]any{}}, "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.pc.WeixinAccount(); got != c.want {
				t.Errorf("WeixinAccount() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestValidateDuplicateWeixinAccount(t *testing.T) {
	cfg := &Config{
		Agents:   []string{"omp"},
		Projects: []ProjectConfig{{Name: "p1"}},
		Defaults: DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{
			{Name: "work", Type: "weixin", Options: map[string]any{}},
			{Name: "work", Type: "weixin", Options: map[string]any{}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate weixin account name")
	}
}

func TestValidateDuplicateWeixinAccountViaOptions(t *testing.T) {
	cfg := &Config{
		Agents:   []string{"omp"},
		Projects: []ProjectConfig{{Name: "p1"}},
		Defaults: DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{
			{Type: "weixin", Options: map[string]any{"account_id": "work"}},
			{Type: "weixin", Options: map[string]any{"account_id": "work"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate weixin account name via options")
	}
}

func TestValidateMultipleWeixinAccounts(t *testing.T) {
	cfg := &Config{
		Agents:   []string{"omp"},
		Projects: []ProjectConfig{{Name: "p1"}},
		Defaults: DefaultsConfig{Agent: "omp", Project: "p1"},
		Platforms: []PlatformConfig{
			{Name: "work", Type: "weixin", Options: map[string]any{}},
			{Name: "personal", Type: "weixin", Options: map[string]any{}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}
