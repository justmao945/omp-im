package omp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/justmao945/omp-im/internal/config"
	"github.com/justmao945/omp-im/internal/core"
)

// acpSession is a single ACP conversation session with the omp agent.
type acpSession struct {
	cfg        config.AgentConfig
	sessionKey string
	tr         *transport

	mu        sync.Mutex
	sessionID string
	history   []historyEntry
}

type historyEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolCall struct {
	Kind string
	Path string
}

// promptResult is returned by session/prompt.
type promptResult struct {
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func newACPSession(ctx context.Context, cfg config.AgentConfig, sessionKey string, tr *transport) (*acpSession, error) {
	s := &acpSession{
		cfg:        cfg,
		sessionKey: sessionKey,
		tr:         tr,
	}
	if err := s.handshake(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *acpSession) handshake(ctx context.Context) error {
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "omp-im",
			"version": "1.0.0",
		},
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
	}
	res, err := s.tr.call(ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("acp initialize: %w", err)
	}
	var initOut struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res, &initOut); err != nil {
		return fmt.Errorf("acp parse initialize: %w", err)
	}
	slog.Debug("acp initialized", "protocol", initOut.ProtocolVersion)

	// Authenticate if the agent advertises auth methods.
	// The "agent" method uses local credentials already configured under ~/.omp.
	// We attempt it unconditionally because many ACP agents require it.
	if _, err := s.tr.call(ctx, "authenticate", map[string]any{"methodId": "agent"}); err != nil {
		slog.Debug("acp authenticate skipped", "error", err)
	}

	newRes, err := s.tr.call(ctx, "session/new", map[string]any{
		"cwd":        s.cfg.WorkDir,
		"mcpServers": []any{},
	})
	if err != nil {
		return fmt.Errorf("acp session/new: %w", err)
	}
	var sn struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newRes, &sn); err != nil {
		return fmt.Errorf("acp parse session/new: %w", err)
	}
	if sn.SessionID == "" {
		return fmt.Errorf("acp session/new returned empty sessionId")
	}
	s.sessionID = sn.SessionID
	slog.Debug("acp session created", "session_id", sn.SessionID, "omp_session", s.sessionKey)
	return nil
}

func (s *acpSession) Respond(ctx context.Context, prompt string, images []core.ImageAttachment) (string, []core.OutboundAttachment, error) {
	if s.sessionID == "" {
		return "", nil, fmt.Errorf("acp: no session id")
	}

	blocks := []any{map[string]any{"type": "text", "text": prompt}}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":      "base64",
				"mediaType": imageMimeType(img.MimeType),
				"data":      base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	params := map[string]any{
		"sessionId": s.sessionID,
		"prompt":    blocks,
	}

	resultCh := make(chan promptResult, 1)
	var textParts []string
	var mu sync.Mutex
	var toolCalls = make(map[string]*toolCall)
	var attachments []core.OutboundAttachment

	handler := func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session/update":
			text := extractAgentText(params)
			if text != "" {
				mu.Lock()
				textParts = append(textParts, text)
				mu.Unlock()
			}
			collectToolCall(params, toolCalls)
			if at := maybeExtractAttachment(params, toolCalls); at != nil {
				mu.Lock()
				attachments = append(attachments, *at)
				mu.Unlock()
			}
		case "request_permission":
			if s.cfg.AutoApproveTools {
				return map[string]any{"optionId": "allow"}, nil
			}
			return map[string]any{"optionId": "deny"}, nil
		}
		return nil, nil
	}

	// Temporarily install the update handler on the shared transport.
	// In a multi-session world this would need per-session routing; for now
	// omp-im creates one transport per session, so this is safe.
	oldHandler := s.tr.serverReqHandler
	s.tr.serverReqHandler = handler
	defer func() { s.tr.serverReqHandler = oldHandler }()

	go func() {
		res, err := s.tr.call(ctx, "session/prompt", params)
		if err != nil {
			slog.Error("acp session/prompt failed", "error", err)
			resultCh <- promptResult{StopReason: "error"}
			return
		}
		var pr promptResult
		_ = json.Unmarshal(res, &pr)
		resultCh <- pr
	}()

	pr := <-resultCh
	if pr.StopReason == "error" {
		return "", nil, fmt.Errorf("acp session/prompt failed")
	}

	mu.Lock()
	reply := strings.TrimSpace(strings.Join(textParts, ""))
	mu.Unlock()

	if reply == "" {
		return "", nil, fmt.Errorf("acp: no assistant text received")
	}

	s.history = append(s.history, historyEntry{Role: "user", Content: prompt})
	s.history = append(s.history, historyEntry{Role: "assistant", Content: reply})
	return reply, attachments, nil
}

func (s *acpSession) Close() error {
	if s.tr == nil {
		return nil
	}
	return s.tr.close()
}

func imageMimeType(mt string) string {
	if mt != "" {
		return mt
	}
	return "image/png"
}

// extractAgentText pulls assistant message text out of a session/update notification.
func extractAgentText(params json.RawMessage) string {
	var wrap struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return ""
	}
	if head.SessionUpdate == "agent_message_chunk" && head.Content.Type == "text" {
		return head.Content.Text
	}
	return ""
}

const maxOutboundAttachmentBytes = 25 << 20

func collectToolCall(params json.RawMessage, calls map[string]*toolCall) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return
	}
	var head struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Kind          string          `json:"kind"`
		RawInput      json.RawMessage `json:"rawInput"`
		RawOutput     json.RawMessage `json:"rawOutput"`
		Status        string          `json:"status"`
		Locations     []struct {
			Path string `json:"path"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return
	}
	switch head.SessionUpdate {
	case "tool_call":
		if head.ToolCallID == "" {
			return
		}
		path := extractPathFromRawInput(head.RawInput)
		if path == "" && len(head.Locations) > 0 {
			path = head.Locations[0].Path
		}
		calls[head.ToolCallID] = &toolCall{
			Kind: head.Kind,
			Path: path,
		}
	case "tool_call_update":
		if head.ToolCallID == "" {
			return
		}
		call := calls[head.ToolCallID]
		if call == nil {
			return
		}
		if resolved := extractResolvedPath(head.RawOutput); resolved != "" {
			call.Path = resolved
		}
		if call.Path == "" && len(head.Locations) > 0 {
			call.Path = head.Locations[0].Path
		}
	}
}

func extractPathFromRawInput(raw json.RawMessage) string {
	var input struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &input)
	return input.Path
}

func extractResolvedPath(raw json.RawMessage) string {
	var out struct {
		Details struct {
			ResolvedPath string `json:"resolvedPath"`
		} `json:"details"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Details.ResolvedPath
}

func maybeExtractAttachment(params json.RawMessage, calls map[string]*toolCall) *core.OutboundAttachment {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return nil
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return nil
	}
	if head.SessionUpdate != "tool_call_update" || head.Status != "completed" {
		return nil
	}
	call := calls[head.ToolCallID]
	if call == nil || call.Path == "" {
		return nil
	}
	// Do not send editor state or unrelated temp files.
	name := strings.ToLower(filepath.Base(call.Path))
	if strings.HasPrefix(name, ".") || strings.Contains(name, "tmp") {
		return nil
	}
	data, err := os.ReadFile(call.Path)
	if err != nil {
		slog.Debug("acp: cannot read tool output file", "path", call.Path, "error", err)
		return nil
	}
	if len(data) == 0 || len(data) > maxOutboundAttachmentBytes {
		return nil
	}
	mime := detectMime(name, data)
	kind := "file"
	if strings.HasPrefix(mime, "image/") || isImageExtension(filepath.Ext(call.Path)) {
		kind = "image"
	}
	return &core.OutboundAttachment{
		Kind:     kind,
		FileName: filepath.Base(call.Path),
		MimeType: mime,
		Data:     data,
	}
}

func isImageExtension(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg":
		return true
	}
	return false
}

func detectMime(name string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		switch strings.TrimPrefix(ext, ".") {
		case "png":
			return "image/png"
		case "jpg", "jpeg":
			return "image/jpeg"
		case "gif":
			return "image/gif"
		case "webp":
			return "image/webp"
		case "svg":
			return "image/svg+xml"
		case "txt":
			return "text/plain"
		case "md":
			return "text/markdown"
		case "pdf":
			return "application/pdf"
		}
	}
	return http.DetectContentType(data)
}
