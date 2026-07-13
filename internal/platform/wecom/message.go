package wecom

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// inboundMessage represents a parsed message from the WeCom WebSocket gateway.
type inboundMessage struct {
	msgid    string // message unique id
	chatid   string // group chat id or user id for direct messages
	chattype string // "single" or "group"
	from     string // sender userid
	msgtype  string // "text", "image", "file", "voice", "mixed", "event", ...
	text     string // concatenated text content
	reqID    string // original frame req_id, used for passive replies
	aibotid  string // robot id, used to strip @-mentions in groups

	// images are decrypted image attachments from this message.
	images []imageAttachment
	// files are decrypted file attachments from this message.
	files []fileAttachment
}

type imageAttachment struct {
	url    string
	aeskey string
}

type fileAttachment struct {
	url      string
	aeskey   string
	filename string
	filetype string
}

// imageContent describes a single image found in a mixed message.
type imageContent struct {
	url    string
	aeskey string
}

// toolRecord stores one tool call for detailed footer display.
type toolRecord struct {
	name   string
	input  string
	output string
	start  time.Time
	end    time.Time
}

// streamSection is an ordered part of the WeCom stream content.
// It preserves the chronological order of text, thinking, and tool events.
type streamSection struct {
	kind string // "text", "thinking", or "tool"
	text string
}

// replyContext stores the data needed to reply to a specific inbound message.
type replyContext struct {
	mu sync.Mutex

	chatid     string
	chattype   string
	reqID      string
	aibotid    string // robot id, used to strip @-mentions in groups
	streamID   string // reused across stream chunks for a single turn
	streamText string // accumulated visible text for concise mode

	// streamBody preserves the chronological order of content in detailed mode.
	streamBody []streamSection

	// streaming state
	thinkingText      string
	thinkingEnd       time.Time
	toolName          string
	toolStart         time.Time
	toolCount         int
	toolTotalDuration time.Duration
	toolHistory       []toolRecord
	turnStart         time.Time
	turnEnd           time.Time
	lastRender        time.Time
	contextUsed       int
	contextSize       int

	stopTicker func() // stops the per-second status-line ticker
	finished   bool   // turn has ended; stop further renders
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
	aibotid, _ := body["aibotid"].(string)
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
		aibotid:  aibotid,
	}

	_, hasQuote := body["quote"]
	slog.Info("wecom: inbound quote metadata", "message_id", msgid, "chatid", chatid, "msgtype", msgtype, "has_quote", hasQuote, "body_fields", messageBodyFields(body))

	text, images, files := parseMessageContent(msgtype, body, aibotid)
	m.text = text
	m.images = images
	m.files = files

	if quote, ok := body["quote"].(map[string]interface{}); ok {
		quoteType, _ := quote["msgtype"].(string)
		quoteText, quoteImages, quoteFiles := parseMessageContent(quoteType, quote, aibotid)
		slog.Info("wecom: received quoted message", "message_id", msgid, "chatid", chatid, "quote_msgtype", quoteType, "quote_content", truncate(quoteText, 200), "quote_images", len(quoteImages), "quote_files", len(quoteFiles))
		m.text = appendQuotedMessage(m.text, quoteText)
		m.images = append(m.images, quoteImages...)
		m.files = append(m.files, quoteFiles...)
	}

	slog.Debug("wecom: parsed inbound message", "msgtype", msgtype, "text_len", len(m.text), "images", len(m.images), "from", fromUser, "aibotid", aibotid)

	return m
}

// messageBodyFields returns sorted top-level fields for safe payload diagnostics.
func messageBodyFields(body map[string]interface{}) []string {
	fields := make([]string, 0, len(body))
	for field := range body {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

// parseMessageContent extracts the text and attachments from a WeCom message
// body. A quoted message uses the same content schema as its parent message.
func parseMessageContent(msgtype string, body map[string]interface{}, aibotid string) (string, []imageAttachment, []fileAttachment) {
	switch msgtype {
	case "text":
		if text, ok := body["text"].(map[string]interface{}); ok {
			content, _ := text["content"].(string)
			return stripWeComAtMentions(content, aibotid), nil, nil
		}
	case "file":
		if f := parseFileContent(body["file"]); f != nil {
			return "[file: " + f.filename + "]", nil, []fileAttachment{*f}
		}
	case "video":
		if f := parseFileContent(body["video"]); f != nil {
			return "[video: " + f.filename + "]", nil, []fileAttachment{*f}
		}
	case "image":
		if img := parseImageContent(body["image"]); img != nil {
			return "[image]", []imageAttachment{*img}, nil
		}
		return "[image]", nil, nil
	case "mixed":
		text, images, files := parseMixedBody(body)
		return stripWeComAtMentions(text, aibotid), images, files
	case "voice":
		if voice, ok := body["voice"].(map[string]interface{}); ok {
			content, _ := voice["content"].(string)
			return stripWeComAtMentions(content, aibotid), nil, nil
		}
	}
	return "", nil, nil
}

func appendQuotedMessage(text, quote string) string {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return text
	}
	if text == "" {
		return "[quoted message]\n" + quote
	}
	return text + "\n\n[quoted message]\n" + quote
}
func parseImageContent(v any) *imageAttachment {
	img, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	url, _ := img["url"].(string)
	if url == "" {
		return nil
	}
	aeskey, _ := img["aeskey"].(string)
	return &imageAttachment{url: url, aeskey: aeskey}
}

func parseMixedBody(body map[string]interface{}) (string, []imageAttachment, []fileAttachment) {
	var textParts []string
	var images []imageAttachment
	var files []fileAttachment
	if mixed, ok := body["mixed"].(map[string]interface{}); ok {
		if items, ok := mixed["msg_item"].([]interface{}); ok {
			for _, item := range items {
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				itemType, _ := itemMap["type"].(string)
				if itemType == "" {
					itemType, _ = itemMap["msgtype"].(string)
				}
				switch itemType {
				case "text":
					if t, ok := itemMap["text"].(map[string]interface{}); ok {
						if content, ok := t["content"].(string); ok && content != "" {
							textParts = append(textParts, content)
						}
					}
				case "image":
					if img := parseImageContent(itemMap["image"]); img != nil {
						images = append(images, *img)
					}
				case "file":
					if f := parseFileContent(itemMap["file"]); f != nil {
						files = append(files, *f)
						if f.filename != "" {
							textParts = append(textParts, "[file: "+f.filename+"]")
						}
					}
				case "video":
					if f := parseFileContent(itemMap["video"]); f != nil {
						files = append(files, *f)
						if f.filename != "" {
							textParts = append(textParts, "[video: "+f.filename+"]")
						}
					}
				}
			}
		}
	}
	if len(images)+len(files) > 0 && len(textParts) == 0 {
		if len(images) > 0 {
			textParts = append(textParts, "[image]")
		} else {
			textParts = append(textParts, "[file]")
		}
	}
	return strings.Join(textParts, "\n"), images, files
}

// parseFileContent parses a file/video attachment body.
func parseFileContent(v any) *fileAttachment {
	f, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	url, _ := f["url"].(string)
	if url == "" {
		return nil
	}
	aeskey, _ := f["aeskey"].(string)
	filename, _ := f["filename"].(string)
	if filename == "" {
		filename, _ = f["name"].(string)
	}
	filetype, _ := f["type"].(string)
	return &fileAttachment{url: url, aeskey: aeskey, filename: filename, filetype: filetype}
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
