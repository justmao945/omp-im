package omp

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func TestACPSessionRespond(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp not in PATH")
	}

	workDir := t.TempDir()
	agent := New()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, err := agent.StartSession(ctx, "weixin:test", core.Project{Name: "default", WorkDir: workDir}, "")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer session.Close()

	reply, _, err := session.Respond(ctx, "what is 2+2? reply with one number", nil, nil, nil)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
	t.Logf("reply: %s", reply)
}

func TestACPSessionMultiTurn(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp not in PATH")
	}

	workDir := t.TempDir()
	agent := New()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	session, err := agent.StartSession(ctx, "weixin:test", core.Project{Name: "default", WorkDir: workDir}, "")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer session.Close()

	_, _, err = session.Respond(ctx, "remember my name is Bob", nil, nil, nil)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	reply, _, err := session.Respond(ctx, "what is my name?", nil, nil, nil)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
	t.Logf("reply: %s", reply)
}

func TestACPSessionGeneratesFile(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp not in PATH")
	}

	workDir := t.TempDir()
	agent := New()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	session, err := agent.StartSession(ctx, "weixin:test", core.Project{Name: "default", WorkDir: workDir}, "")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer session.Close()

	path := filepath.Join(workDir, "hello-omp.txt")
	prompt := fmt.Sprintf("create a file at %s with content 'hello from omp' and reply only with the absolute path", path)
	reply, attachments, err := session.Respond(ctx, prompt, nil, nil, nil)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
	t.Logf("reply: %s", reply)

	found := false
	for _, a := range attachments {
		if a.Kind == "file" && strings.Contains(a.FileName, "hello-omp.txt") {
			found = true
			if string(a.Data) != "hello from omp" {
				t.Fatalf("attachment content = %q, want %q", a.Data, "hello from omp")
			}
		}
	}
	if !found {
		t.Fatalf("expected file attachment, got %+v", attachments)
	}
}
