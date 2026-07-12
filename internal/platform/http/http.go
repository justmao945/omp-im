// Package http provides a test-only HTTP platform for omp-im.
// It exposes a POST endpoint that delivers messages to the engine, so you can
// test agents locally without a real IM platform.
//
// Use this in config.json:
//
//	{"type": "http", "options": {"addr": ":8080"}}
//
// Then send messages:
//
//	curl -X POST http://localhost:8080/send \
//	  -H 'Content-Type: application/json' \
//	  -d '{"session_key":"test:u1","user_id":"u1","content":"hello"}'
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// Platform is an HTTP test platform that receives messages via POST /send.
type Platform struct {
	addr    string
	handler core.MessageHandler

	mu      sync.Mutex
	server  *http.Server
	started bool
	stopCh  chan struct{}
	replyCh chan string
}

// New creates an HTTP platform from options.
// Supported options:
//   - addr: listen address (default ":8080")
func New(opts map[string]any) (*Platform, error) {
	addr := ":8080"
	if v, ok := opts["addr"].(string); ok && v != "" {
		addr = v
	}
	return &Platform{addr: addr, stopCh: make(chan struct{}), replyCh: make(chan string, 1)}, nil
}

// Name returns the platform name.
func (p *Platform) Name() string { return "http" }

// Start starts the HTTP server and registers the message handler.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return fmt.Errorf("http platform already started")
	}
	p.handler = handler

	mux := http.NewServeMux()
	mux.HandleFunc("/send", p.handleSend)
	p.server = &http.Server{Addr: p.addr, Handler: mux}
	p.started = true

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.started = false
		p.mu.Unlock()
		return fmt.Errorf("listen %s: %w", p.addr, err)
	}
	p.mu.Unlock()

	slog.Info("http platform listening", "addr", p.addr)

	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("http platform server error", "error", err)
		}
	}()

	<-p.stopCh
	return nil
}

// Stop shuts down the HTTP server.
func (p *Platform) Stop() error {
	p.mu.Lock()
	if !p.started || p.server == nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	close(p.stopCh)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// Reply sends a text reply back to the caller via the HTTP response channel.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	select {
	case p.replyCh <- content:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Platform) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionKey string `json:"session_key"`
		UserID     string `json:"user_id"`
		Content    string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}
	if req.SessionKey == "" {
		req.SessionKey = "http:u1"
	}
	if req.UserID == "" {
		req.UserID = "u1"
	}

	if p.handler == nil {
		http.Error(w, "engine not ready", http.StatusServiceUnavailable)
		return
	}

	// Drain stale reply.
	select {
	case <-p.replyCh:
	default:
	}

	msg := &core.Message{
		SessionKey: req.SessionKey,
		Platform:   "http",
		UserID:     req.UserID,
		Content:    req.Content,
		ReplyCtx:   req.SessionKey,
	}
	p.handler(p, msg)

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	select {
	case reply := <-p.replyCh:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(reply))
	case <-ctx.Done():
		http.Error(w, "timeout waiting for reply", http.StatusGatewayTimeout)
	}
}
