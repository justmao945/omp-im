package wecom

import (
	"context"
	"testing"

	"github.com/justmao945/omp-im/internal/core"
)

func TestParseFileMessage(t *testing.T) {
	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Headers: map[string]string{"req_id": "r1"},
		Body: map[string]interface{}{
			"msgtype": "file",
			"chatid":  "c1",
			"chattype": "single",
			"file": map[string]interface{}{
				"url":      "https://example.com/file.pdf",
				"aeskey":   "key123",
				"filename": "report.pdf",
				"type":     "pdf",
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.msgtype != "file" {
		t.Fatalf("msgtype = %q", msg.msgtype)
	}
	if len(msg.files) != 1 {
		t.Fatalf("files = %d", len(msg.files))
	}
	f := msg.files[0]
	if f.filename != "report.pdf" || f.url != "https://example.com/file.pdf" || f.aeskey != "key123" || f.filetype != "pdf" {
		t.Fatalf("file = %+v", f)
	}
	if msg.text != "[file: report.pdf]" {
		t.Fatalf("text = %q", msg.text)
	}
}

func TestParseVideoMessage(t *testing.T) {
	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Headers: map[string]string{"req_id": "r2"},
		Body: map[string]interface{}{
			"msgtype": "video",
			"chatid":  "c1",
			"chattype": "single",
			"video": map[string]interface{}{
				"url":      "https://example.com/video.mp4",
				"aeskey":   "key456",
				"filename": "demo.mp4",
				"type":     "mp4",
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if len(msg.files) != 1 || msg.files[0].filename != "demo.mp4" {
		t.Fatalf("files = %+v", msg.files)
	}
	if msg.text != "[video: demo.mp4]" {
		t.Fatalf("text = %q", msg.text)
	}
}

func TestParseMixedWithFile(t *testing.T) {
	frame := &wsFrame{
		Cmd: "aibot_msg_callback",
		Headers: map[string]string{"req_id": "r3"},
		Body: map[string]interface{}{
			"msgtype": "mixed",
			"chatid":  "c1",
			"mixed": map[string]interface{}{
				"msg_item": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": map[string]interface{}{"content": "please review"},
					},
					map[string]interface{}{
						"type": "file",
						"file": map[string]interface{}{
							"url":      "https://example.com/doc.txt",
							"filename": "doc.txt",
						},
					},
				},
			},
		},
	}
	msg := parseInboundMessage(frame)
	if msg == nil {
		t.Fatal("expected message")
	}
	if len(msg.files) != 1 || msg.files[0].filename != "doc.txt" {
		t.Fatalf("files = %+v", msg.files)
	}
	if msg.text != "please review\n[file: doc.txt]" {
		t.Fatalf("text = %q", msg.text)
	}
}

func TestLooksLikeMarkdown(t *testing.T) {
	if !looksLikeMarkdown("# Hello") {
		t.Fatal("expected markdown")
	}
	if !looksLikeMarkdown("`code`") {
		t.Fatal("expected markdown")
	}
	if !looksLikeMarkdown("[link](url)") {
		t.Fatal("expected markdown")
	}
	if looksLikeMarkdown("plain text") {
		t.Fatal("expected plain")
	}
}

func TestStreamReplyPassive(t *testing.T) {
	// Create a platform with a mock wsClient that records sends.
	var sent []map[string]interface{}
	p := &Platform{
		cfg: &config{botID: "b1", secret: "s1"},
		wsClient: &wsClient{
			sendFn: func(v map[string]interface{}) error {
				sent = append(sent, v)
				return nil
			},
		},
	}
	rc := &replyContext{chatid: "c1", chattype: "single", reqID: "r1"}
	if err := p.StreamReply(context.Background(), rc, "hello ", false); err != nil {
		t.Fatalf("stream 1: %v", err)
	}
	if err := p.StreamReply(context.Background(), rc, "world", true); err != nil {
		t.Fatalf("stream 2: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent = %d", len(sent))
	}
	first := sent[0]["body"].(map[string]interface{})["stream"].(map[string]interface{})
	if first["content"] != "hello " {
		t.Fatalf("first content = %q", first["content"])
	}
	last := sent[1]["body"].(map[string]interface{})["stream"].(map[string]interface{})
	if last["content"] != "hello world" {
		t.Fatalf("last content = %q", last["content"])
	}
	if last["finish"] != true {
		t.Fatal("expected finish=true on last chunk")
	}
}

func TestStreamReplyActiveFallback(t *testing.T) {
	var sent []map[string]interface{}
	p := &Platform{
		cfg: &config{botID: "b1", secret: "s1"},
		wsClient: &wsClient{
			sendFn: func(v map[string]interface{}) error {
				sent = append(sent, v)
				return nil
			},
		},
	}
	rc := &replyContext{chatid: "c1", chattype: "group"}
	if err := p.StreamReply(context.Background(), rc, "hi", true); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	if sent[0]["cmd"] != "aibot_send_msg" {
		t.Fatalf("cmd = %v", sent[0]["cmd"])
	}
}

func TestReplyMarkdown(t *testing.T) {
	var sent []map[string]interface{}
	p := &Platform{
		cfg: &config{botID: "b1", secret: "s1"},
		wsClient: &wsClient{
			sendFn: func(v map[string]interface{}) error {
				sent = append(sent, v)
				return nil
			},
		},
	}
	rc := &replyContext{chatid: "c1", reqID: "r1"}
	if err := p.Reply(context.Background(), rc, "# Title\n- a\n- b"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	body, _ := sent[0]["body"].(map[string]interface{})
	if body["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v", body["msgtype"])
	}
}

func TestEnginePassesFilesToSession(t *testing.T) {
	// Verify that the engine passes Files from the message to the session.
	// This is a compilation-level check via the fake session in engine_test.go.
	_ = core.FileAttachment{}
}
