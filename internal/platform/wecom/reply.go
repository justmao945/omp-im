package wecom

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"
)

// maxStreamContentBytes is the maximum content length for a single WeCom stream message.
const maxStreamContentBytes = 20480

// sendTextReply sends a passive text reply to the chat that triggered the inbound message.
// Long text is split into multiple stream chunks with the same stream id.
func (p *Platform) sendTextReply(rc *replyContext, text string) error {
	if rc == nil || rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if text == "" {
		return nil
	}

	chunks := splitTextChunks(text, maxStreamContentBytes)
	streamID := generateReqID()
	for i, chunk := range chunks {
		body := map[string]interface{}{
			"msgtype": "stream",
			"stream": map[string]interface{}{
				"id":      streamID,
				"finish":  i == len(chunks)-1,
				"content": chunk,
			},
		}
		if err := p.respond(rc.reqID, body); err != nil {
			return err
		}
	}
	return nil
}

// StreamReply implements core.StreamReplyer. It sends a single stream chunk for
// the current turn. The first call initializes the shared stream id, and the
// finished chunk signals the end of the turn.
func (p *Platform) StreamReply(ctx context.Context, replyCtx any, delta string, finished bool) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if rc.streamID == "" {
		rc.streamID = generateReqID()
	}
	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      rc.streamID,
			"finish":  finished,
			"content": delta,
		},
	}
	if rc.reqID != "" {
		if err := p.respond(rc.reqID, body); err != nil {
			return err
		}
	} else {
		if err := p.sendActiveMessage(rc.chatid, rc.chattype, body); err != nil {
			return err
		}
	}
	if finished {
		slog.Debug("wecom: finished stream reply")
	}
	return nil
}

// splitTextChunks splits text into chunks so each chunk is at most maxBytes bytes
// when encoded as UTF-8. It never splits a multi-byte rune.
func splitTextChunks(text string, maxBytes int) []string {
	if maxBytes <= 0 {
		return []string{text}
	}
	var chunks []string
	var current []rune
	var currentBytes int
	for _, r := range text {
		rBytes := utf8.RuneLen(r)
		if rBytes < 0 {
			rBytes = 1
		}
		if currentBytes+rBytes > maxBytes && currentBytes > 0 {
			chunks = append(chunks, string(current))
			current = nil
			currentBytes = 0
		}
		current = append(current, r)
		currentBytes += rBytes
	}
	if len(current) > 0 {
		chunks = append(chunks, string(current))
	}
	return chunks
}

// respond sends an aibot_respond_msg frame using the original inbound req_id.
func (p *Platform) respond(reqID string, body map[string]interface{}) error {
	payload := map[string]interface{}{
		"cmd": "aibot_respond_msg",
		"headers": map[string]string{
			"req_id": reqID,
		},
		"body": body,
	}

	if err := p.wsClient.send(payload); err != nil {
		return err
	}
	slog.Debug("wecom: sent reply")
	return nil
}

// sendActiveMessage is a fallback for active push (aibot_send_msg) when passive reply is not possible.
func (p *Platform) sendActiveMessage(chatid, chattype string, body map[string]interface{}) error {
	if chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}

	chatTypeInt := 1
	if chattype == "group" {
		chatTypeInt = 2
	}

	payload := map[string]interface{}{
		"cmd": "aibot_send_msg",
		"headers": map[string]string{
			"req_id": generateReqID(),
		},
		"body": map[string]interface{}{
			"chatid":    chatid,
			"chat_type": chatTypeInt,
		},
	}
	for k, v := range body {
		payload["body"].(map[string]interface{})[k] = v
	}
	return p.wsClient.send(payload)
}
