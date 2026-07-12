package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAgent struct {
	name         string
	reply        string
	attachments  []OutboundAttachment
	err          error
	started      int
	mu           sync.Mutex
	respondDelay time.Duration
}

func (a *fakeAgent) Name() string { return a.name }
func (a *fakeAgent) Stop() error  { return nil }
func (a *fakeAgent) StartSession(ctx context.Context, sessionKey string, project Project, resumeSessionID string) (AgentSession, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.mu.Lock()
	a.started++
	delay := a.respondDelay
	a.mu.Unlock()
	sid := resumeSessionID
	if sid == "" {
		sid = "fake-" + sessionKey
	}
	return &fakeSession{reply: a.reply, attachments: a.attachments, project: project, delay: delay, sessionID: sid}, nil
}
func (a *fakeAgent) Started() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

type fakeSession struct {
	reply        string
	attachments  []OutboundAttachment
	project      Project
	closed       bool
	delay        time.Duration
	history      []HistoryEntry
	inputTokens  int
	outputTokens int
	sessionID    string
}

func (s *fakeSession) SessionID() string {
	return s.sessionID
}

func (s *fakeSession) Respond(ctx context.Context, prompt string, images []ImageAttachment, files []FileAttachment, onEvent func(StreamEvent)) (string, []OutboundAttachment, error) {
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	reply := s.reply + ":" + prompt
	s.history = append(s.history, HistoryEntry{Role: "user", Content: prompt})
	s.history = append(s.history, HistoryEntry{Role: "assistant", Content: reply})
	s.inputTokens = len(prompt)
	s.outputTokens = len(reply)
	return reply, s.attachments, nil
}

func (s *fakeSession) Status() AgentStatus {
	return AgentStatus{State: "idle", InputTokens: s.inputTokens, OutputTokens: s.outputTokens}
}
func (s *fakeSession) History() []HistoryEntry {
	return s.history
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
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "again",
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

	if agent.Started() != 1 {
		t.Fatalf("started %d sessions, want 1", agent.Started())
	}

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}
}

func TestEngineSessionCreationFailure(t *testing.T) {
	agent := &fakeAgent{name: "fake", err: errors.New("boom")}
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
	if len(replies) != 1 || !strings.Contains(replies[0], "Failed to start session") {
		t.Fatalf("replies = %v", replies)
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
	if replies[0] != "Switched agent to **claude**. Takes effect on the next message." {
		t.Fatalf("first reply = %q", replies[0])
	}
	if replies[len(replies)-1] != "claude-reply:hello" {
		t.Fatalf("last reply = %q, want claude-reply:hello", replies[len(replies)-1])
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

	if len(replies) != 1 || !strings.Contains(replies[0], "Unknown command") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEngineEscCommand(t *testing.T) {
	a1 := &fakeAgent{name: "slow", reply: "slow-reply", respondDelay: 10 * time.Second}
	eng := NewEngine(
		map[string]Agent{"slow": a1},
		"slow",
		map[string]Project{"default": {Name: "default", WorkDir: "/tmp"}},
		"default",
	)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		// Send a long-running message, then immediately send /esc to cancel it.
		go p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "please wait",
			ReplyCtx:   "ctx",
		})
		time.Sleep(50 * time.Millisecond)
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/esc",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 1 || !strings.Contains(replies[0], "cancelled") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEngineSessionStore(t *testing.T) {
	eng, agent := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	dir := t.TempDir()
	storePath := filepath.Join(dir, "sessions.db")
	if err := eng.SetSessionStore(storePath); err != nil {
		t.Fatal(err)
	}
	// Pre-populate a session ID so we can verify resume across restarts.
	eng.setSessionID("fake:u1", "persisted-id-123")
	if err := eng.SetSessionStore(storePath); err != nil {
		t.Fatal(err)
	}

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

	if agent.Started() != 1 {
		t.Fatalf("agent started %d times, want 1", agent.Started())
	}
}

func TestEnginePCommand(t *testing.T) {
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
			Content:    "/p",
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

	if !strings.Contains(replies[0], "**Agent:**") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEnginePCommandShowsStatus(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		// First message creates the session and populates usage/history.
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "hello",
			ReplyCtx:   "ctx",
		})
		time.Sleep(50 * time.Millisecond)
		// /p should now include status and token info, not raw history.
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/p",
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

	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}
	pReply := replies[1]
	if !strings.Contains(pReply, "**Agent:**") {
		t.Fatalf("/p reply missing agent: %q", pReply)
	}
	if !strings.Contains(pReply, "**Project:**") {
		t.Fatalf("/p reply missing project: %q", pReply)
	}
	if strings.Contains(pReply, "Status:") || strings.Contains(pReply, "**Elapsed:**") || strings.Contains(pReply, "**Tools used:**") || strings.Contains(pReply, "**Current tool:**") || strings.Contains(pReply, "**Command:**") || strings.Contains(pReply, "**Tokens:**") {
		t.Fatalf("/p reply should not contain elapsed/tools/command/tokens: %q", pReply)
	}
}

func TestEngineNewCommand(t *testing.T) {
	eng, _ := newTestEngine("fake")
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		// First message creates a session.
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "hello",
			ReplyCtx:   "ctx",
		})
		time.Sleep(20 * time.Millisecond)
		// /new closes the session.
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/new",
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

	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}
	if !strings.Contains(replies[1], "New session created") {
		t.Fatalf("new reply = %q", replies[1])
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

	if len(replies) != 1 || !strings.Contains(replies[0], "Switched project") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEnginePCommandDuringActiveTurn(t *testing.T) {
	a1 := &fakeAgent{name: "slow", reply: "slow-reply", respondDelay: 5 * time.Second}
	eng := NewEngine(
		map[string]Agent{"slow": a1},
		"slow",
		map[string]Project{"default": {Name: "default", WorkDir: "/tmp"}},
		"default",
	)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		go p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "please wait",
			ReplyCtx:   "ctx",
		})
		time.Sleep(50 * time.Millisecond)
		p.getHandler()(p, &Message{
			SessionKey: "fake:u1",
			Platform:   "fake",
			UserID:     "u1",
			Content:    "/p",
			ReplyCtx:   "ctx",
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	// /p should reply immediately while the slow turn is still queued/active.
	if !strings.Contains(replies[0], "**Agent:**") {
		t.Fatalf("replies = %v", replies)
	}
}

func TestEngineMessageOrderingPerSession(t *testing.T) {
	a1 := &fakeAgent{name: "count", reply: "ok"}
	eng := NewEngine(
		map[string]Agent{"count": a1},
		"count",
		map[string]Project{"default": {Name: "default", WorkDir: "/tmp"}},
		"default",
	)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		for p.getHandler() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		// Send several messages sequentially (mirroring platform dispatch).
		// The engine queues them and processes them in the same order.
		for i := 1; i <= 5; i++ {
			p.getHandler()(p, &Message{
				SessionKey: "fake:u1",
				Platform:   "fake",
				UserID:     "u1",
				Content:    fmt.Sprintf("msg%d", i),
				ReplyCtx:   "ctx",
			})
		}
	}()

	done := make(chan struct{})
	go func() {
		_ = eng.Run()
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	_ = eng.Stop()
	<-done

	p.mu.Lock()
	replies := append([]string(nil), p.replies...)
	p.mu.Unlock()

	if len(replies) != 5 {
		t.Fatalf("got %d replies, want 5", len(replies))
	}
	for i, r := range replies {
		want := fmt.Sprintf("ok:msg%d", i+1)
		if r != want {
			t.Fatalf("reply[%d] = %q, want %q", i, r, want)
		}
	}
}

func TestFormatContext(t *testing.T) {
	cases := []struct {
		used, size int
		want       string
	}{
		{20332, 262144, "8% / 262K"},
		{53000, 200000, "26% / 200K"},
		{1500000, 2000000, "75% / 2.0M"},
		{500, 900, "56% / 900"},
	}
	for _, c := range cases {
		got := formatContext(c.used, c.size)
		if got != c.want {
			t.Errorf("formatContext(%d, %d) = %q, want %q", c.used, c.size, got, c.want)
		}
	}
}


