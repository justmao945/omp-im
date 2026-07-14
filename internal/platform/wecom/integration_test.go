package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justmao945/omp-im/internal/core"
)

// mockWeComGateway is a test WebSocket server that mimics the WeCom AI bot gateway.
type mockWeComGateway struct {
	server      *http.Server
	upgrader    websocket.Upgrader
	mu          sync.Mutex
	conn        *websocket.Conn
	subscribed  bool
	messages    []map[string]interface{}
	replies     []map[string]interface{}
	frameCh     chan map[string]interface{}
}

func newMockGateway(addr string) *mockWeComGateway {
	m := &mockWeComGateway{
		frameCh: make(chan map[string]interface{}, 10),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handle)
	m.server = &http.Server{Addr: addr, Handler: mux}
	return m
}

func (m *mockWeComGateway) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.conn = nil
		m.mu.Unlock()
		conn.Close()
	}()

	for {
		var frame map[string]interface{}
		if err := conn.ReadJSON(&frame); err != nil {
			return
		}

		cmd, _ := frame["cmd"].(string)
		if cmd == wsCmdSubscribe {
			m.mu.Lock()
			m.subscribed = true
			m.mu.Unlock()
			_ = conn.WriteJSON(map[string]interface{}{
				"headers": map[string]string{"req_id": "auth-1"},
				"errcode": 0,
				"errmsg":  "ok",
			})
	} else if cmd == "aibot_respond_msg" || cmd == "aibot_send_msg" {
			m.mu.Lock()
			m.replies = append(m.replies, frame)
			m.mu.Unlock()
			m.frameCh <- frame
			// Send ack so writeAndWaitAck completes promptly.
			headers, _ := frame["headers"].(map[string]interface{})
			reqID, _ := headers["req_id"].(string)
			_ = conn.WriteJSON(map[string]interface{}{
				"headers": map[string]string{"req_id": reqID},
				"errcode": 0,
				"errmsg":  "ok",
			})
		} else if cmd == wsCmdPing {
			_ = conn.WriteJSON(map[string]interface{}{
				"headers": map[string]string{"req_id": "pong"},
				"errcode": 0,
				"errmsg":  "ok",
			})
		}
	}
}

func (m *mockWeComGateway) sendInboundMessage(chatid, chattype, text, reqID string) error {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.WriteJSON(map[string]interface{}{
		"cmd": "aibot_msg_callback",
		"headers": map[string]string{"req_id": reqID},
		"body": map[string]interface{}{
			"msgid":    "m1",
			"chatid":   chatid,
			"chattype": chattype,
			"msgtype":  "text",
			"from":     map[string]interface{}{"userid": "u1"},
			"text":     map[string]interface{}{"content": text},
		},
	})
}

func (m *mockWeComGateway) start() error {
	go func() { _ = m.server.ListenAndServe() }()
	return nil
}

func (m *mockWeComGateway) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return m.server.Shutdown(ctx)
}

func TestWebsocketEndToEnd(t *testing.T) {
	gw := newMockGateway("127.0.0.1:18081")
	if err := gw.start(); err != nil {
		t.Fatal(err)
	}
	defer gw.stop()

	time.Sleep(100 * time.Millisecond)

	p, err := New(map[string]any{
		"bot_id":        "test-bot",
		"secret":        "test-secret",
		"websocket_url": "ws://127.0.0.1:18081",
	})
	if err != nil {
		t.Fatal(err)
	}

	var got *core.Message
	done := make(chan struct{})
	if err := p.Start(func(platform core.Platform, msg *core.Message) {
		got = msg
		_ = platform.Reply(context.Background(), msg.ReplyCtx, msg.Content)
		close(done)
	}); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	// Wait for subscription.
	for range 50 {
		gw.mu.Lock()
		sub := gw.subscribed
		gw.mu.Unlock()
		if sub {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	gw.mu.Lock()
	if !gw.subscribed {
		gw.mu.Unlock()
		t.Fatal("not subscribed")
	}
	gw.mu.Unlock()

	if err := gw.sendInboundMessage("group1", "group", "hello bot", "req-456"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	if got == nil || got.SessionKey != "wecom:group1" {
		t.Fatalf("session key = %q", got.SessionKey)
	}

	select {
	case reply := <-gw.frameCh:
		body, _ := reply["body"].(map[string]interface{})
		stream, _ := body["stream"].(map[string]interface{})
		if stream["content"] != "hello bot" {
			t.Fatalf("reply content = %q", stream["content"])
		}
		headers, _ := reply["headers"].(map[string]interface{})
		if headers["req_id"] != "req-456" {
			t.Fatalf("reply req_id = %q", headers["req_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for reply")
	}
}

func TestMarshalStreamBody(t *testing.T) {
	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      "s1",
			"finish":  true,
			"content": "hi",
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	stream, _ := decoded["stream"].(map[string]interface{})
	if stream["content"] != "hi" {
		t.Fatalf("content = %q", stream["content"])
	}
}
