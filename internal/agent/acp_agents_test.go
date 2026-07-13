package agent

import "testing"

func TestResolveACPCommands(t *testing.T) {
	tests := []struct {
		name string
		fn   func() (string, []string)
		want string
	}{
		{name: "omp", fn: resolveOMPCommand, want: "omp"},
		{name: "claude", fn: resolveClaudeCommand, want: "claude-agent-acp"},
		{name: "codex", fn: resolveCodexCommand, want: "codex-acp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := tt.fn()
			if got != tt.want {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewSupportsAllBuiltInAgents(t *testing.T) {
	for _, name := range []string{"omp", "claude", "codex"} {
		a, err := New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if a.Name() != name {
			t.Fatalf("New(%q).Name() = %q", name, a.Name())
		}
	}
}
