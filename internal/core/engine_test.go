package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeAgent struct {
	reply       string
	attachments []OutboundAttachment
	err         error
}

func (a *fakeAgent) Name() string { return "fake" }
func (a *fakeAgent) Stop() error  { return nil }
func (a *fakeAgent) StartSession(ctx context.Context, sessionKey string) (AgentSession, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &fakeSession{reply: a.reply, attachments: a.attachments}, nil
}

type fakeSession struct {
	reply       string
	attachments []OutboundAttachment
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
	p.handler = h
	p.mu.Unlock()
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

func TestEngineRoutesMessage(t *testing.T) {
	agent := &fakeAgent{reply: "hi"}
	eng := NewEngine(agent)
	p := &fakePlatform{name: "fake"}
	eng.AddPlatform(p)

	go func() {
		// Wait for Start to register the handler, then simulate a message.
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

	// Run a short time then stop.
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
	agent := &fakeAgent{reply: "ok"}
	eng := NewEngine(agent)

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
}

func TestEngineSessionCreationFailure(t *testing.T) {
	agent := &fakeAgent{err: errors.New("boom")}
	eng := NewEngine(agent)
	_, err := eng.getOrCreateSession(context.Background(), "k1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEngineSendsAttachments(t *testing.T) {
	agent := &fakeAgent{
		reply: "here",
		attachments: []OutboundAttachment{
			{Kind: "image", FileName: "a.png", MimeType: "image/png", Data: []byte("png-bytes")},
			{Kind: "file", FileName: "b.txt", MimeType: "text/plain", Data: []byte("text-bytes")},
		},
	}

	eng := NewEngine(agent)
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
