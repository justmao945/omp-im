package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/agent"
	"github.com/justmao945/omp-im/internal/core"
)

func TestHTTPPlatformEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp not in PATH")
	}

	workDir := t.TempDir()
	agents := map[string]core.Agent{}
	a, err := agent.New("omp")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agents["omp"] = a

	projects := map[string]core.Project{"default": {Name: "default", WorkDir: workDir}}
	engine := core.NewEngine(agents, "omp", projects, "default")

	p, err := New(map[string]any{"addr": ":0"})
	if err != nil {
		t.Fatalf("create platform: %v", err)
	}
	engine.AddPlatform(p)

	go func() {
		if err := engine.Run(); err != nil {
			t.Errorf("engine run: %v", err)
		}
	}()
	t.Cleanup(func() { _ = engine.Stop() })

	// Wait for platform to start.
	time.Sleep(500 * time.Millisecond)

	// First send a real message to create a session.
	sendRequest := func(content string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"session_key": "test:u1",
			"user_id":     "u1",
			"content":     content,
		})
		req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		p.handleSend(w, req)
		return w
	}

	// Send a hello message to create the session.
	w := sendRequest("what is 2+2? reply with one number")
	if w.Code != http.StatusOK {
		t.Fatalf("hello response status = %d, body = %q", w.Code, w.Body.String())
	}
	if w.Body.String() == "" {
		t.Fatal("empty reply for hello")
	}
	t.Logf("hello reply: %s", w.Body.String())

	// Now send /p to check status includes model.
	w = sendRequest("/p")
	if w.Code != http.StatusOK {
		t.Fatalf("/p response status = %d, body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	t.Logf("/p reply: %s", body)
	if body == "" {
		t.Fatal("empty /p reply")
	}
	if !strings.Contains(body, "Model:") {
		t.Fatalf("/p reply missing Model: %q", body)
	}
}
