package wecom

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// Platform implements core.Platform for WeCom AI bot via WebSocket long connection.
type Platform struct {
	cfg        *config
	wsClient   *wsClient
	handler    core.MessageHandler
	httpClient *http.Client

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// New creates a WeCom platform from options.
// Required options: bot_id, secret.
// Optional: websocket_url (default wss://openws.work.weixin.qq.com), allow_from, group_allow_from.
func New(opts map[string]any) (*Platform, error) {
	cfg, err := parseConfig(opts)
	if err != nil {
		return nil, err
	}

	p := &Platform{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	p.wsClient = newWSClient(cfg, p.handleFrame)
	return p, nil
}

// StreamingEnabled reports whether the engine should send incremental replies.
func (p *Platform) StreamingEnabled() bool { return p.cfg.stream }

// FooterEnabled reports whether the turn-summary footer should be appended.
func (p *Platform) FooterEnabled() bool { return p.cfg.footer }

// Name returns the platform name.
func (p *Platform) Name() string { return "wecom" }

// Start connects to the WeCom AI bot gateway and starts processing messages.
func (p *Platform) Start(handler core.MessageHandler) error {
	var startErr error
	p.startOnce.Do(func() {
		if handler == nil {
			startErr = fmt.Errorf("wecom: handler is nil")
			return
		}
		p.handler = handler

		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			if err := p.wsClient.run(ctx); err != nil {
				slog.Error("wecom: websocket client exited", "error", err)
			}
		}()
	})
	return startErr
}

// Stop closes the WebSocket connection.
func (p *Platform) Stop() error {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		_ = p.wsClient.stop()
		p.wg.Wait()
	})
	return nil
}

// Reply sends text back to the chat identified by replyCtx.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: missing chatid in reply context")
	}
	_ = ctx
	if looksLikeMarkdown(content) && len(content) <= maxMarkdownContentBytes {
		return p.sendMarkdownReply(rc, content)
	}
	return p.sendTextReply(rc, content)
}

// maxMarkdownContentBytes is a conservative size limit for a single markdown message.
const maxMarkdownContentBytes = 4096

// sendMarkdownReply sends a passive markdown reply to the chat that triggered the inbound message.
func (p *Platform) sendMarkdownReply(rc *replyContext, text string) error {
	if rc == nil || rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}
	if text == "" {
		return nil
	}
	body := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]interface{}{
			"content": text,
		},
	}
	if rc.reqID != "" {
		return p.respond(rc.reqID, body)
	}
	return p.sendActiveMessage(rc.chatid, rc.chattype, body)
}

// looksLikeMarkdown returns true if the text contains common markdown syntax.
func looksLikeMarkdown(s string) bool {
	for _, marker := range []string{"#", "*", "`", "[", ">![", "|", "-", ">", "~~~"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// handleFrame is called by the websocket client for every inbound frame.
func (p *Platform) handleFrame(frame *wsFrame) {
	if frame == nil {
		return
	}

	if frame.Cmd != "aibot_msg_callback" {
		slog.Debug("wecom: skipping non-message frame", "cmd", frame.Cmd)
		return
	}

	msg := parseInboundMessage(frame)
	if msg == nil {
		return
	}

	if !p.isAllowed(msg) {
		slog.Debug("wecom: message not allowed", "from", msg.from, "chatid", msg.chatid, "chattype", msg.chattype)
		return
	}

	if msg.text == "" {
		if msg.msgtype != "text" && msg.msgtype != "mixed" && msg.msgtype != "voice" && msg.msgtype != "image" {
			slog.Debug("wecom: unsupported message type", "msgtype", msg.msgtype)
			return
		}
	}

	if msg.chattype == "group" {
		slog.Info("wecom: received group message", "message_id", msg.msgid, "chatid", msg.chatid, "from", msg.from, "msgtype", msg.msgtype)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	images := p.collectInboundImages(ctx, msg)
	slog.Debug("wecom: collected inbound images", "msgtype", msg.msgtype, "expected", len(msg.images), "collected", len(images))

	sessionKey := fmt.Sprintf("wecom:%s", msg.chatid)
	files := p.collectInboundFiles(context.Background(), msg)
	coreMsg := &core.Message{
		SessionKey: sessionKey,
		Platform:   p.Name(),
		MessageID:  msg.msgid,
		ChannelID:  msg.chatid,
		UserID:     msg.from,
		Content:    msg.text,
		Images:     images,
		Files:      files,
		ReplyCtx:   &replyContext{chatid: msg.chatid, chattype: msg.chattype, reqID: msg.reqID, aibotid: msg.aibotid},
	}

	if p.handler != nil {
		p.handler(p, coreMsg)
	}
}

// isAllowed checks dm and group allowlists.
func (p *Platform) isAllowed(msg *inboundMessage) bool {
	if msg.chattype == "group" {
		return p.cfg.allowGroup(msg.chatid)
	}
	return p.cfg.allowUser(msg.from)
}

// Ping sends a ping and waits briefly for the connection to be alive.
// This is used in tests to verify the mock connection is established.
func (p *Platform) Ping() error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn := p.wsClient.currentConn(); conn != nil {
			return p.wsClient.ping(conn)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("wecom: not connected")
}

// WaitForMessage is used in tests to receive a message.
func (p *Platform) WaitForMessage(timeout time.Duration) (*core.Message, error) {
	// This is a placeholder; tests use a custom handler instead.
	return nil, fmt.Errorf("not implemented")
}
