package wecom

import (
	"fmt"
	"log/slog"
)

// sendTextReply sends a passive text reply to the chat that triggered the inbound message.
func (p *Platform) sendTextReply(rc *replyContext, text string) error {
	if rc == nil || rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if text == "" {
		return nil
	}

	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      generateReqID(),
			"finish":  true,
			"content": text,
		},
	}
	return p.respond(rc.reqID, body)
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
