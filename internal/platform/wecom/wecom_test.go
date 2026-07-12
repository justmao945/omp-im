package wecom

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func TestParseConfigRequiresBotIDAndSecret(t *testing.T) {
	if _, err := parseConfig(map[string]any{}); err == nil {
		t.Fatal("expected error for missing bot_id and secret")
	}
	if _, err := parseConfig(map[string]any{"bot_id": "b"}); err == nil {
		t.Fatal("expected error for missing secret")
	}
	if _, err := parseConfig(map[string]any{"secret": "s"}); err == nil {
		t.Fatal("expected error for missing bot_id")
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(map[string]any{"bot_id": "b", "secret": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.botID != "b" || cfg.secret != "s" {
		t.Fatalf("botID/secret = %q/%q", cfg.botID, cfg.secret)
	}
	if cfg.websocketURL != "wss://openws.work.weixin.qq.com" {
		t.Fatalf("websocketURL = %q", cfg.websocketURL)
	}
}

func TestAllowList(t *testing.T) {
	if !allowList("*", "any") {
		t.Fatal("* should allow all")
	}
	if !allowList("", "any") {
		t.Fatal("empty should allow all")
	}
	if !allowList("u1,u2", "u1") {
		t.Fatal("u1 should be allowed")
	}
	if allowList("u1,u2", "u3") {
		t.Fatal("u3 should not be allowed")
	}
}

func TestParseInboundMessage(t *testing.T) {
	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Body: map[string]interface{}{
			"msgid":    "m1",
			"chatid":   "g1",
			"chattype": "group",
			"msgtype":  "text",
			"from": map[string]interface{}{
				"userid": "u1",
			},
			"text": map[string]interface{}{
				"content": "hello",
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.msgid != "m1" || msg.chatid != "g1" || msg.chattype != "group" || msg.from != "u1" || msg.text != "hello" {
		t.Fatalf("message mismatch: %+v", msg)
	}
}

func TestParseInboundMessageSkipsEvents(t *testing.T) {
	frame := &wsFrame{Cmd: "aibot_event_callback"}
	if parseInboundMessage(frame) != nil {
		t.Fatal("expected nil for event frame")
	}
}

func TestParseMixedMessage(t *testing.T) {
	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Body: map[string]interface{}{
			"msgid":    "m2",
			"chatid":   "u2",
			"chattype": "single",
			"msgtype":  "mixed",
			"from": map[string]interface{}{
				"userid": "u2",
			},
			"mixed": map[string]interface{}{
				"msg_item": []interface{}{
					map[string]interface{}{
						"msgtype": "text",
						"text":    map[string]interface{}{"content": "hi"},
					},
					map[string]interface{}{
						"msgtype": "text",
						"text":    map[string]interface{}{"content": "there"},
					},
				},
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.text != "hi\nthere" {
		t.Fatalf("text = %q", msg.text)
	}
}

func TestPlatformName(t *testing.T) {
	p, err := New(map[string]any{"bot_id": "b", "secret": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "wecom" {
		t.Fatalf("name = %q", p.Name())
	}
}

func TestSessionKeyPerChat(t *testing.T) {
	p, err := New(map[string]any{"bot_id": "b", "secret": "s"})
	if err != nil {
		t.Fatal(err)
	}

	var got *core.Message
	done := make(chan struct{})
	handler := func(_ core.Platform, msg *core.Message) {
		got = msg
		close(done)
	}
	if err := p.Start(handler); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Body: map[string]interface{}{
			"msgid":    "m3",
			"chatid":   "group123",
			"chattype": "group",
			"msgtype":  "text",
			"from":     map[string]interface{}{"userid": "u3"},
			"text":     map[string]interface{}{"content": "group msg"},
		},
	}
	p.handleFrame(frame)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}

	if got.SessionKey != "wecom:group123" {
		t.Fatalf("session key = %q", got.SessionKey)
	}
	if got.ChannelID != "group123" {
		t.Fatalf("channel id = %q", got.ChannelID)
	}
	if got.UserID != "u3" {
		t.Fatalf("user id = %q", got.UserID)
	}
	if got.Content != "group msg" {
		t.Fatalf("content = %q", got.Content)
	}

	rc, ok := got.ReplyCtx.(*replyContext)
	if !ok || rc.chatid != "group123" || rc.chattype != "group" {
		t.Fatalf("reply context mismatch: %+v", got.ReplyCtx)
	}
}

func TestAllowFromGroup(t *testing.T) {
	cfg, _ := parseConfig(map[string]any{
		"bot_id":         "b",
		"secret":         "s",
		"group_allow_from": "g1,g2",
	})
	p := &Platform{cfg: cfg}

	if !p.isAllowed(&inboundMessage{chattype: "group", chatid: "g1"}) {
		t.Fatal("g1 should be allowed")
	}
	if p.isAllowed(&inboundMessage{chattype: "group", chatid: "g3"}) {
		t.Fatal("g3 should not be allowed")
	}
	if !p.isAllowed(&inboundMessage{chattype: "single", from: "u1"}) {
		t.Fatal("single messages should be allowed when allow_from is empty")
	}
}

func TestSendTextReply(t *testing.T) {
	var sent map[string]interface{}
	p := &Platform{
		cfg: &config{botID: "b", secret: "s"},
		wsClient: &wsClient{
			sendFn: func(payload map[string]interface{}) error {
				sent = payload
				return nil
			},
		},
	}

	rc := &replyContext{chatid: "chat1", chattype: "group", reqID: "req-123"}
	if err := p.sendTextReply(rc, "hello"); err != nil {
		t.Fatal(err)
	}
	if sent["cmd"] != "aibot_respond_msg" {
		t.Fatalf("cmd = %q", sent["cmd"])
	}
	headers, _ := sent["headers"].(map[string]string)
	if headers["req_id"] != "req-123" {
		t.Fatalf("req_id = %q", headers["req_id"])
	}
	body, _ := sent["body"].(map[string]interface{})
	if body["msgtype"] != "stream" {
		t.Fatalf("msgtype = %q", body["msgtype"])
	}
	stream, _ := body["stream"].(map[string]interface{})
	if stream["finish"] != true || stream["content"] != "hello" {
		t.Fatalf("stream mismatch: %+v", stream)
	}
}

func TestReplyWithEmptyContent(t *testing.T) {
	var sent bool
	p := &Platform{
		cfg: &config{botID: "b", secret: "s"},
		wsClient: &wsClient{
			sendFn: func(payload map[string]interface{}) error {
				sent = true
				return nil
			},
		},
	}
	if err := p.Reply(nil, &replyContext{chatid: "c1"}, ""); err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Fatal("expected no message for empty content")
	}
}

func TestGenerateReqID(t *testing.T) {
	id1 := generateReqID()
	id2 := generateReqID()
	if id1 == id2 {
		t.Fatal("req ids should be unique")
	}
}

func TestFrameJSON(t *testing.T) {
	raw := `{"cmd":"aibot_msg_callback","body":{"msgid":"m","chatid":"c","chattype":"single","msgtype":"text","from":{"userid":"u"},"text":{"content":"x"}}}`
	var frame wsFrame
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Cmd != "aibot_msg_callback" {
		t.Fatalf("cmd = %q", frame.Cmd)
	}
	msg := parseInboundMessage(&frame)
	if msg == nil || msg.text != "x" {
		t.Fatalf("message mismatch: %+v", msg)
	}
}
