package acp

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func TestExtractModelFromConfigOptions(t *testing.T) {
	opts := []configOption{
		{ID: "mode", Name: "Session Mode", Category: "mode", Type: "select", CurrentValue: "code"},
		{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "claude-4-20250514"},
		{ID: "thought_level", Name: "Thinking", Category: "thought_level", Type: "select", CurrentValue: "high"},
	}
	if got := extractModel(opts); got != "claude-4-20250514" {
		t.Fatalf("extractModel = %q, want claude-4-20250514", got)
	}
}

func TestExtractConfigOptionUpdate(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gpt-5"}]}}`)
	opts := extractConfigOptionUpdate(params)
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if got := extractModel(opts); got != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", got)
	}
}

func TestExtractConfigOptionUpdateIgnoresOtherUpdates(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"text_update","text":"hello"}}`)
	opts := extractConfigOptionUpdate(params)
	if len(opts) != 0 {
		t.Fatalf("got %d options, want 0", len(opts))
	}
}

func TestModelPreservedAcrossTurnStatusReset(t *testing.T) {
	s := &Session{
		agentStatus: core.AgentStatus{State: "idle", Model: "kimi-code/kimi-for-coding"},
	}
	s.startTurnStatus()
	if s.agentStatus.Model != "kimi-code/kimi-for-coding" {
		t.Fatalf("startTurnStatus dropped model: %q", s.agentStatus.Model)
	}
	s.resetStatus()
	if s.agentStatus.Model != "kimi-code/kimi-for-coding" {
		t.Fatalf("resetStatus dropped model: %q", s.agentStatus.Model)
	}
}

func TestModelPreservedOnConfigOptionUpdate(t *testing.T) {
	s := &Session{
		agentStatus: core.AgentStatus{State: "idle", Model: "old-model"},
	}
	opts := []configOption{
		{ID: "model", Category: "model", CurrentValue: "new-model"},
	}
	s.setModelFromConfigOptions(opts)
	if s.agentStatus.Model != "new-model" {
		t.Fatalf("model = %q, want new-model", s.agentStatus.Model)
	}
}

func TestModelDetectedOnRealSession(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp not in PATH")
	}

	workDir := t.TempDir()
	cfg := Config{Command: "omp", Args: []string{"acp"}, WorkDir: workDir, AutoApproveTools: true}
	tr, err := NewTransport(cfg, nil)
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer tr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewSession(ctx, cfg, "test:u1", "", tr)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer s.Close()

	st := s.Status()
	t.Logf("status: %+v", st)
	if st.Model == "" {
		t.Fatalf("model not detected; status = %+v", st)
	}
}



