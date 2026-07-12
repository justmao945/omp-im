package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAgent struct {
	name        string
	reply       string
	attachments []OutboundAttachment
	err         error
	started     int
	mu          sync.Mutex
}

func (a *fakeAgent) Name() string { return a.name }
func (a *fakeAgent) Stop() error  { return nil }
func (a *fakeAgent) StartSession(ctx context.Context, sessionKey string, project Project) (AgentSession, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.mu.Lock()
	a.started++
	a.mu.Unlock()
	return &fakeSession{reply: a.reply, attachments: a.attachments, project: project}, nil
}
func (a *fakeAgent) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	return []SessionInfo{}, nil
}

func (a *fakeAgent) Started() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

type fakeSession struct {
	reply       string
	attachments []OutboundAttachment
	project     Project
	closed      bool
}

func (s *fakeSession) Respond(ctx context.Context, prompt string, images []ImageAttachment) (string, []OutboundAttachment, error) {
	return s.reply + ":" + prompt, s.attachments, nil
}
func (s *fakeSession) Close() error {
	s.closed = true
	return nil
}

type fakePlatform struct {
	name      string
	mu        sync.Mutex
	handler   MessageHandler
	replies   []string
	replyErrs []error
	images    []ImageAttachment
	files     []FileAttachment
}

func (p *fakePlatform) Name() string { return p.name }
func (p *fakePlatform) Start(h MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = h
	return nil
}

func (p *fakePlatform) getHandler() MessageHandler {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handler
}
func (p *fakePlatform) Stop() error { return nil }
func (p *fakePlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, content)
	if len(p.replyErrs) > 0 {
		err := p.replyErrs[0]
		p.replyErrs = p.replyErrs[1:]
		return err
	}
	return nil
}

func (p *fakePlatform) SendImage(ctx context.Context, replyCtx any, img ImageAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, img)
	return nil
}

func (p *fakePlatform) SendFile(ctx context.Context, replyCtx any, file FileAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files = append(p.files, file)
	return nil
}

func newTestEngine(agentName string) (*Engine, *fakeAgent) {
	agent := &fakeAgent{name: agentName, reply: "hi"}
	agents := map[string]Agent{agentName: agent}
	projects := map[string]Project{"default": {Name: "default", WorkDir: "/tmp"}}
	return NewEngine(agents, agentName, projects, "default"), agent
}

func TestEngineRoutesMessage(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "hello",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if replies[0] != "hi:hello" {
		t.Fatalf("reply = %q, want hi:hello", replies[0])
	}
}

func TestEngineSessionReuse(t *testing.T) {
	eng, agent := newTestEngine("fake")

	s1, err := eng.getOrCreateSession(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := eng.getOrCreateSession(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if s1 != s2 {
		t.Fatal("expected same session for same key")
	}
	if agent.Started() != 1 {
		t.Fatalf("started %d sessions, want 1", agent.Started())
	}
}

func TestEngineSessionCreationFailure(t *testing.T) {
	agent := &fakeAgent{name: "fake", err: errors.New("boom")}
	eng := NewEngine(map[string]Agent{"fake": agent}, "fake", map[string]Project{"default": {Name: "default"}}, "default")
	_, err := eng.getOrCreateSession(context.Background(), "k1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEngineSendsAttachments(t *testing.T) {
	agent := &fakeAgent{
		name:  "fake",
		reply: "here",
		attachments: []OutboundAttachment{
			{Kind: "image", FileName: "a.png", MimeType: "image/png", Data: []byte("png-bytes")},
			{Kind: "file", FileName: "b.txt", MimeType: "text/plain", Data: []byte("text-bytes")},
		},
	}
	eng := NewEngine(map[string]Agent{"fake": agent}, "fake", map[string]Project{"default": {Name: "default"}}, "default")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "send files",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	images := append([]ImageAttachment(nil), p.images...)
	files := append([]FileAttachment(nil), p.files...)
	p.mu.Unlock()

	if len(images) != 1 || string(images[0].Data) != "png-bytes" {
		t.Fatalf("images = %+v", images)
	}
	if len(files) != 1 || string(files[0].Data) != "text-bytes" {
		t.Fatalf("files = %+v", files)
	}
}

func TestEngineAgentCommand(t *testing.T) {
	a1 := &fakeAgent{name: "omp", reply: "omp-reply"}
	a2 := &fakeAgent{name: "claude", reply: "claude-reply"}
	eng := NewEngine(
		map[string]Agent{"omp": a1, "claude": a2},
		"omp",
		map[string]Project{"default": {Name: "default"}},
		"default",
	)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/agent claude",
			ReplyCtx:   "ctx",
		})
		// give command time to process, then send normal message
		time.Sleep(50 * time.Millisecond)
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "hello",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) < 2 {
		t.Fatalf("got %d replies, want at least 2", len(replies))
	}
	if replies[0] != "已切换 agent 为 claude，下条消息生效" {
		t.Fatalf("first reply = %q", replies[0])
	}
	if replies[len(replies)-1] != "claude-reply:hello" {
		t.Fatalf("last reply = %q, want claude-reply:hello", replies[len(replies)-1])
	}
}

func TestEngineListCommand(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/list",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if !strings.Contains(replies[0], "Agent fake 的 sessions") {
		t.Fatalf("list reply = %q", replies[0])
	}
}

func TestEngineHelpCommand(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/help",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if !strings.Contains(replies[0], "/agent") {
		t.Fatalf("help reply = %q", replies[0])
	}
}

func TestEngineUnknownCommand(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/foo",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 || !strings.Contains(replies[0], "未知命令") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEngineProjCommand(t *testing.T) {
	eng := NewEngine(
		map[string]Agent{"fake": &fakeAgent{name: "fake", reply: "hi"}},
		"fake",
		map[string]Project{"default": {Name: "default"}, "other": {Name: "other", WorkDir: "/tmp/other"}},
		"default",
	)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/proj other",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 || !strings.Contains(replies[0], "已切换 project") {
		t.Fatalf("replies = %v", replies)
	}
}
