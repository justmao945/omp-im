package wecom

import "strings"

// inboundMessage represents a parsed message from the WeCom WebSocket gateway.
type inboundMessage struct {
	msgid    string
	chatid   string // group chat id or user id for direct messages
	chattype string // "single" or "group"
	from     string // sender userid
	msgtype  string // "text", "image", "file", "voice", "mixed", "event", ...
	text     string // concatenated text content
	reqID    string // original frame req_id, used for passive replies

	// images are downloaded/decrypted media URLs. Not implemented in MVP.
	images []imageAttachment
}

type imageAttachment struct {
	url    string
	aeskey string
}

// replyContext stores the data needed to reply to a specific inbound message.
type replyContext struct {
	chatid   string
	chattype string
	reqID    string
}

// wsFrame is the top-level envelope received over the WebSocket.
type wsFrame struct {
	Cmd     string                 `json:"cmd"`
	Headers map[string]string      `json:"headers"`
	Body    map[string]interface{} `json:"body"`
	ErrCode int                    `json:"errcode"`
	ErrMsg  string                 `json:"errmsg"`
}

// isSuccess returns true if the frame has no error code or errcode == 0.
func (f *wsFrame) isSuccess() bool {
	return f.ErrCode == 0
}

// parseInboundMessage extracts a simple text message from a WebSocket frame.
// It returns nil if the frame is not a user text message.
func parseInboundMessage(frame *wsFrame) *inboundMessage {
	if frame.Cmd != "aibot_msg_callback" {
		return nil
	}

	body := frame.Body
	msgid, _ := body["msgid"].(string)
	chatid, _ := body["chatid"].(string)
	chattype, _ := body["chattype"].(string)
	msgtype, _ := body["msgtype"].(string)
	reqID, _ := frame.Headers["req_id"]

	fromUser := ""
	if from, ok := body["from"].(map[string]interface{}); ok {
		fromUser, _ = from["userid"].(string)
	}

	if chattype == "" {
		chattype = "single"
	}
	if chatid == "" {
		chatid = fromUser
	}

	m := &inboundMessage{
		msgid:    msgid,
		chatid:   chatid,
		chattype: chattype,
		from:     fromUser,
		msgtype:  msgtype,
		reqID:    reqID,
	}

	switch msgtype {
	case "text":
		if text, ok := body["text"].(map[string]interface{}); ok {
			m.text, _ = text["content"].(string)
		}
	case "mixed":
		m.text = extractMixedText(body)
	case "voice":
		if voice, ok := body["voice"].(map[string]interface{}); ok {
			m.text, _ = voice["content"].(string)
		}
	}

	return m
}

// extractMixedText concatenates text parts from a mixed message body.
func extractMixedText(body map[string]interface{}) string {
	if mixed, ok := body["mixed"].(map[string]interface{}); ok {
		if items, ok := mixed["msg_item"].([]interface{}); ok {
			var parts []string
			for _, item := range items {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if t, ok := itemMap["text"].(map[string]interface{}); ok {
						if content, ok := t["content"].(string); ok && content != "" {
							parts = append(parts, content)
						}
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
