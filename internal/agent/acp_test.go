package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
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
	if got := extractConfigOptionValue(opts, "model"); got != "claude-4-20250514" {
		t.Fatalf("extractModel = %q, want claude-4-20250514", got)
	}
}

func TestExtractConfigOptionUpdate(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gpt-5"}]}}`)
	opts := extractConfigOptionUpdate(params)
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if got := extractConfigOptionValue(opts, "model"); got != "gpt-5" {
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
	defer tr.Close()

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

func TestNewTransportReportsInstallHintForMissingCommand(t *testing.T) {
	_, err := NewTransport(Config{
		Command:     "omp-im-test-missing-acp-command",
		InstallHint: "install it with: npm install -g example-acp",
	}, nil)
	if err == nil {
		t.Fatal("expected missing command error")
	}
	if !strings.Contains(err.Error(), "npm install -g example-acp") {
		t.Fatalf("error = %q, want installation guidance", err)
	}
}

func TestExtractUsageUpdate(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"usage_update","used":53000,"size":200000}}`)
	used, size := extractUsageUpdate(params)
	if used != 53000 || size != 200000 {
		t.Fatalf("usage update = %d/%d, want 53000/200000", used, size)
	}
}

func TestExtractUsageUpdateIgnoresOtherUpdates(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"text_update","text":"hello"}}`)
	used, size := extractUsageUpdate(params)
	if used != 0 || size != 0 {
		t.Fatalf("usage update = %d/%d, want 0/0", used, size)
	}
}

func TestSetModelFromConfigOptionsExtractsThoughtLevel(t *testing.T) {
	s := &Session{agentStatus: core.AgentStatus{State: "idle"}}
	opts := []configOption{
		{ID: "model", Category: "model", CurrentValue: "gpt-5"},
		{ID: "thought_level", Category: "thought_level", CurrentValue: "high"},
	}
	s.setModelFromConfigOptions(opts)
	if s.agentStatus.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", s.agentStatus.Model)
	}
	if s.agentStatus.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", s.agentStatus.ReasoningEffort)
	}
}

func TestStatusSnapshotPreservesSessionFields(t *testing.T) {
	s := &Session{agentStatus: core.AgentStatus{
		State:           "idle",
		Model:           "m",
		ReasoningEffort: "high",
		ContextUsed:     100,
		ContextSize:     200,
	}}
	s.startTurnStatus()
	if s.agentStatus.Model != "m" || s.agentStatus.ReasoningEffort != "high" || s.agentStatus.ContextUsed != 100 || s.agentStatus.ContextSize != 200 {
		t.Fatalf("startTurnStatus dropped session fields: %+v", s.agentStatus)
	}
	s.resetStatus()
	if s.agentStatus.Model != "m" || s.agentStatus.ReasoningEffort != "high" || s.agentStatus.ContextUsed != 100 || s.agentStatus.ContextSize != 200 {
		t.Fatalf("resetStatus dropped session fields: %+v", s.agentStatus)
	}
}

func TestExtractToolCommandFromRawInput(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"1","kind":"execute","rawInput":{"command":"ls -la","workdir":"/tmp"}}}`)
	if got := toolCallCommand(params); got != "ls -la" {
		t.Fatalf("command = %q, want %q", got, "ls -la")
	}
	params = []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"2","kind":"read","rawInput":{"path":"/etc/passwd"}}}`)
	if got := toolCallPath(params); got != "/etc/passwd" {
		t.Fatalf("path = %q, want %q", got, "/etc/passwd")
	}
	if got := extractToolRawInput(params); got == "" {
		t.Fatal("raw input empty")
	}
}

func TestExtractAgentThought(t *testing.T) {
	params := []byte(`{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"I should check the file first."}}}`)
	if got := extractAgentThought(params); got != "I should check the file first." {
		t.Fatalf("got %q, want %q", got, "I should check the file first.")
	}
	if got := extractAgentText(params); got != "" {
		t.Fatalf("extractAgentText should not return thought text, got %q", got)
	}
}

func TestParseMCPServersForACP(t *testing.T) {
	servers, err := parseMCPServers([]byte(`{
		"mcpServers": {
			"shell": {
				"command": "/usr/local/bin/mcp-shell",
				"args": ["serve"],
				"env": {"TOKEN": "secret"}
			},
			"wecom": {
				"type": "http",
				"url": "https://example.test/mcp",
				"headers": {"Authorization": "Bearer token"}
			},
			"disabled": {"type": "http", "url": "https://disabled.test", "enabled": false}
		}
	}`))
	if err != nil {
		t.Fatalf("parse MCP servers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("server count = %d, want 2", len(servers))
	}

	byName := make(map[string]map[string]any, len(servers))
	for _, server := range servers {
		value, ok := server.(map[string]any)
		if !ok {
			t.Fatalf("server type = %T, want map[string]any", server)
		}
		byName[value["name"].(string)] = value
	}
	if got := byName["shell"]["command"]; got != "/usr/local/bin/mcp-shell" {
		t.Fatalf("stdio command = %v", got)
	}
	if got := byName["wecom"]["url"]; got != "https://example.test/mcp" {
		t.Fatalf("HTTP URL = %v", got)
	}
	if _, ok := byName["disabled"]; ok {
		t.Fatal("disabled MCP server was included")
	}
}

func TestResolveMCPPath(t *testing.T) {
	if got := resolveMCPPath("/opt/server", "./mcp/server.mjs"); got != "/opt/server/mcp/server.mjs" {
		t.Fatalf("relative MCP path = %q", got)
	}
	if got := resolveMCPPath("/opt/server", "node"); got != "node" {
		t.Fatalf("PATH command = %q", got)
	}
	if got := resolveMCPPath("/opt/server", "/usr/local/bin/server"); got != "/usr/local/bin/server" {
		t.Fatalf("absolute MCP path = %q", got)
	}
}

func TestRedactRPCPayload(t *testing.T) {
	got := redactRPCPayload([]byte(`{"command":"printf ok","apiKey":"hidden","nested":{"cookie":"hidden","visible":"yes"}}`))
	value := got.(map[string]any)
	if value["command"] != "printf ok" {
		t.Fatalf("command was changed: %v", value["command"])
	}
	if value["apiKey"] != "[REDACTED]" {
		t.Fatalf("API key was not redacted: %v", value["apiKey"])
	}
	nested := value["nested"].(map[string]any)
	if nested["cookie"] != "[REDACTED]" || nested["visible"] != "yes" {
		t.Fatalf("nested redaction = %#v", nested)
	}
}

func TestRPCMessagePreservesZeroID(t *testing.T) {
	var received rpcMessage
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{}}`), &received); err != nil {
		t.Fatalf("unmarshal RPC message: %v", err)
	}
	if !received.hasID || received.ID != 0 {
		t.Fatalf("received ID = %d, present = %t; want present zero ID", received.ID, received.hasID)
	}

	response, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: received.ID, hasID: true, Result: mustMarshal(map[string]any{})})
	if err != nil {
		t.Fatalf("marshal RPC response: %v", err)
	}
	if string(response) != `{"jsonrpc":"2.0","id":0,"result":{}}` {
		t.Fatalf("response = %s, want id:0 preserved", response)
	}
}

func TestMCPWarmProxyCachesDiscoveryAndForwardsCalls(t *testing.T) {
	calls := make(map[string]int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		calls[request.Method]++
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "ping", "description": "ping", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}
		default:
			t.Fatalf("unexpected upstream method %q", request.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer upstream.Close()

	proxy := newMCPWarmProxy()
	defer proxy.Close()
	servers, err := proxy.warmHTTPServers(context.Background(), []any{map[string]any{
		"name": "test",
		"type": "http",
		"url":  upstream.URL,
	}})
	if err != nil {
		t.Fatalf("warm HTTP MCP: %v", err)
	}
	proxyURL := servers[0].(map[string]any)["url"].(string)

	post := func(method string) map[string]any {
		body := []byte(`{"jsonrpc":"2.0","id":9,"method":"` + method + `","params":{}}`)
		response, err := http.Post(proxyURL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post %s: %v", method, err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read %s response: %v", method, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("decode %s response: %v", method, err)
		}
		return decoded
	}
	if got := post("initialize")["id"]; got != float64(9) {
		t.Fatalf("cached initialize response ID = %v", got)
	}
	if got := post("tools/list")["id"]; got != float64(9) {
		t.Fatalf("cached tools/list response ID = %v", got)
	}
	if calls["initialize"] != 1 || calls["tools/list"] != 1 {
		t.Fatalf("discovery was forwarded after warming: %#v", calls)
	}
	post("tools/call")
	if calls["tools/call"] != 1 {
		t.Fatalf("tools/call was not forwarded: %#v", calls)
	}
}
