package agent

import (
	"bytes"
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
	"time"
	"unicode/utf8"

	"github.com/justmao945/omp-im/internal/core"
)

// Config carries the runtime parameters for spawning an ACP agent process.
type Config struct {
	Command          string
	Args             []string
	WorkDir          string
	AutoApproveTools bool
	// AuthMethod is sent to authenticate during initialization. Empty leaves
	// authentication to the spawned agent's own CLI credentials.
	AuthMethod string
	// InstallHint explains how to install Command when it is unavailable.
	InstallHint string
}

// Session is a single ACP conversation session with a local agent.
type Session struct {
	cfg        Config
	sessionKey string
	tr         *Transport

	mu               sync.Mutex
	turnMu           sync.Mutex
	statusMu         sync.Mutex
	agentStatus      core.AgentStatus
	turnStart        time.Time
	toolCount        int
	currentTool      time.Time
	sessionID        string
	resumeSessionID  string
	history          []historyEntry
	currentStatus    string
	lastActivityAt   time.Time
	OnClose          func()
	capLoadSession   bool
	capResumeSession bool
}

type historyEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolCall struct {
	Kind string
	Path string
}

type configOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	CurrentValue any    `json:"currentValue"`
}

func extractConfigOptionValue(opts []configOption, category string) string {
	for _, opt := range opts {
		if opt.Category == category || opt.ID == category {
			if s, ok := opt.CurrentValue.(string); ok {
				return s
			}
		}
	}
	return ""
}

func (s *Session) setModelFromConfigOptions(opts []configOption) {
	if m := extractConfigOptionValue(opts, "model"); m != "" {
		s.statusMu.Lock()
		s.agentStatus.Model = m
		s.statusMu.Unlock()
		slog.Info("acp: model detected", "session", s.sessionKey, "model", m)
	}
	if r := extractConfigOptionValue(opts, "thought_level"); r != "" {
		s.statusMu.Lock()
		s.agentStatus.ReasoningEffort = r
		s.statusMu.Unlock()
		slog.Info("acp: thought_level detected", "session", s.sessionKey, "thought_level", r)
	}
}

func extractConfigOptionUpdate(params json.RawMessage) []configOption {
	var wrap struct {
		Update struct {
			SessionUpdate string         `json:"sessionUpdate"`
			ConfigOptions []configOption `json:"configOptions"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return nil
	}
	if wrap.Update.SessionUpdate != "config_option_update" {
		return nil
	}
	return wrap.Update.ConfigOptions
}

type usageUpdate struct {
	Used int `json:"used"`
	Size int `json:"size"`
}

func extractUsageUpdate(params json.RawMessage) (used, size int) {
	var wrap struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Used          int    `json:"used"`
			Size          int    `json:"size"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return 0, 0
	}
	if wrap.Update.SessionUpdate != "usage_update" {
		return 0, 0
	}
	return wrap.Update.Used, wrap.Update.Size
}

func (s *Session) setUsageUpdate(used, size int) {
	if size <= 0 {
		return
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.agentStatus.ContextUsed = used
	s.agentStatus.ContextSize = size
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

func NewSession(ctx context.Context, cfg Config, sessionKey string, resumeSessionID string, tr *Transport) (*Session, error) {
	s := &Session{
		cfg:             cfg,
		sessionKey:      sessionKey,
		resumeSessionID: resumeSessionID,
		tr:              tr,
		currentStatus:   "idle",
		lastActivityAt:  time.Now(),
	}
	if err := s.handshake(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) handshake(ctx context.Context) error {
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
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(res, &initOut); err != nil {
		return fmt.Errorf("acp parse initialize: %w", err)
	}
	s.capLoadSession = initOut.AgentCapabilities.LoadSession
	s.capResumeSession = len(initOut.AgentCapabilities.SessionCapabilities.Resume) > 0
	slog.Debug("acp initialized", "protocol", initOut.ProtocolVersion, "load_session", s.capLoadSession, "resume_session", s.capResumeSession)

	// Some ACP adapters rely on the local CLI's own login state and do not need
	// an ACP authenticate request.
	if s.cfg.AuthMethod != "" {
		if _, err := s.tr.call(ctx, "authenticate", map[string]any{"methodId": s.cfg.AuthMethod}); err != nil {
			slog.Debug("acp authenticate skipped", "method", s.cfg.AuthMethod, "error", err)
		}
	}

	// If we have a previously persisted session ID, try to resume it first.
	if s.resumeSessionID != "" {
		if s.capResumeSession {
			if err := s.callSessionResume(ctx); err == nil {
				return nil
			} else {
				slog.Warn("acp session/resume failed, starting new session", "session", s.sessionKey, "error", err)
			}
		} else if s.capLoadSession {
			if err := s.callSessionLoad(ctx); err == nil {
				return nil
			} else {
				slog.Warn("acp session/load failed, starting new session", "session", s.sessionKey, "error", err)
			}
		} else {
			slog.Warn("acp session persistence not supported by agent, starting new session", "session", s.sessionKey)
		}
	}

	newRes, err := s.tr.call(ctx, "session/new", map[string]any{
		"cwd":        s.cfg.WorkDir,
		"mcpServers": []any{},
	})
	if err != nil {
		return fmt.Errorf("acp session/new: %w", err)
	}
	var sn struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err := json.Unmarshal(newRes, &sn); err != nil {
		return fmt.Errorf("acp parse session/new: %w", err)
	}
	if sn.SessionID == "" {
		return fmt.Errorf("acp session/new returned empty sessionId")
	}
	s.sessionID = sn.SessionID
	s.setModelFromConfigOptions(sn.ConfigOptions)
	slog.Info("acp session created", "session_id", sn.SessionID, "omp_session", s.sessionKey, "model", s.agentStatus.Model)
	return nil
}

func (s *Session) callSessionResume(ctx context.Context) error {
	res, err := s.tr.call(ctx, "session/resume", map[string]any{
		"sessionId":  s.resumeSessionID,
		"cwd":        s.cfg.WorkDir,
		"mcpServers": []any{},
	})
	if err != nil {
		return err
	}
	var out struct {
		ConfigOptions []configOption `json:"configOptions"`
		Model         string         `json:"model"`
	}
	_ = json.Unmarshal(res, &out)
	s.sessionID = s.resumeSessionID
	s.setModelFromConfigOptions(out.ConfigOptions)
	if out.Model != "" {
		s.statusMu.Lock()
		s.agentStatus.Model = out.Model
		s.statusMu.Unlock()
	}
	slog.Info("acp session resumed", "session_id", s.sessionID, "omp_session", s.sessionKey, "model", s.agentStatus.Model)
	return nil
}

func (s *Session) callSessionLoad(ctx context.Context) error {
	res, err := s.tr.call(ctx, "session/load", map[string]any{
		"sessionId":  s.resumeSessionID,
		"cwd":        s.cfg.WorkDir,
		"mcpServers": []any{},
	})
	if err != nil {
		return err
	}
	var out struct {
		ConfigOptions []configOption `json:"configOptions"`
		Model         string         `json:"model"`
	}
	_ = json.Unmarshal(res, &out)
	s.sessionID = s.resumeSessionID
	s.setModelFromConfigOptions(out.ConfigOptions)
	if out.Model != "" {
		s.statusMu.Lock()
		s.agentStatus.Model = out.Model
		s.statusMu.Unlock()
	}
	slog.Info("acp session loaded", "session_id", s.sessionID, "omp_session", s.sessionKey, "model", s.agentStatus.Model)
	return nil
}

func (s *Session) SessionID() string {
	return s.sessionID
}

func (s *Session) Respond(ctx context.Context, prompt string, images []core.ImageAttachment, files []core.FileAttachment, onEvent func(core.StreamEvent)) (string, []core.OutboundAttachment, error) {
	if s.sessionID == "" {
		return "", nil, fmt.Errorf("acp: no session id")
	}
	// Serialize full turns per session so concurrent inbound messages from
	// the same user are processed (and replied to) in order.
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	s.setStatus("busy")
	defer s.setStatus("idle")
	s.startTurnStatus()
	defer s.resetStatus()

	emit := func(ev core.StreamEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}

	blocks := []any{map[string]any{"type": "text", "text": buildPromptWithFiles(prompt, files)}}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type":     "image",
			"data":     base64.StdEncoding.EncodeToString(img.Data),
			"mimeType": imageMimeType(img.MimeType),
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
				emit(core.StreamEvent{Type: "text", Text: text, Status: s.Status()})
				mu.Lock()
				textParts = append(textParts, text)
				mu.Unlock()
			}
			thinking := extractAgentThought(params)
			if thinking != "" {
				emit(core.StreamEvent{Type: "thinking", Text: thinking, Status: s.Status()})
			}
			if opts := extractConfigOptionUpdate(params); len(opts) > 0 {
				s.setModelFromConfigOptions(opts)
			}
			if used, size := extractUsageUpdate(params); size > 0 {
				s.setUsageUpdate(used, size)
				emit(core.StreamEvent{Type: "usage", Status: s.Status()})
			}
			if hasToolCall(params) {
				cmd := toolCallCommand(params)
				if cmd == "" {
					cmd = toolCallPath(params)
				}
				if cmd == "" {
					cmd = toolCallKind(params)
				}
				slog.Info("acp: tool call started", "session", s.sessionKey, "kind", toolCallKind(params), "path", toolCallPath(params), "command", truncate(cmd, 200))
				s.setToolStatus(true, cmd)
				emit(core.StreamEvent{Type: "tool_start", Tool: cmd, ToolInput: extractToolRawInput(params), Status: s.Status()})
			}
			if isToolCallCompleted(params) {
				slog.Info("acp: tool call completed", "session", s.sessionKey)
				emit(core.StreamEvent{Type: "tool_end", ToolOutput: extractToolRawOutput(params), Status: s.Status()})
				s.setToolStatus(false, "")
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
	s.setUsage(pr)

	mu.Lock()
	reply := strings.TrimSpace(strings.Join(textParts, ""))
	mu.Unlock()

	slog.Debug("acp: assembled reply", "reply", reply, "attachments", len(attachments))

	if reply == "" {
		return "", nil, fmt.Errorf("acp: no assistant text received")
	}

	s.history = append(s.history, historyEntry{Role: "user", Content: prompt})
	s.history = append(s.history, historyEntry{Role: "assistant", Content: reply})
	s.touch()
	return reply, attachments, nil
}

func (s *Session) setStatus(status string) {
	s.mu.Lock()
	old := s.currentStatus
	s.currentStatus = status
	s.lastActivityAt = time.Now()
	s.mu.Unlock()
	if old != status {
		slog.Info("acp: session status changed", "session", s.sessionKey, "from", old, "to", status)
	}
}

func (s *Session) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivityAt = time.Now()
}

func (s *Session) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentStatus
}

func (s *Session) lastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivityAt
}

func (s *Session) Status() core.AgentStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	st := s.agentStatus
	if st.State == "idle" {
		return st
	}
	st.TurnDuration = time.Since(s.turnStart)
	if !s.currentTool.IsZero() {
		st.CurrentToolDuration = time.Since(s.currentTool)
	}
	return st
}

func (s *Session) History() []core.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.HistoryEntry, len(s.history))
	for i, h := range s.history {
		out[i] = core.HistoryEntry{Role: h.Role, Content: h.Content}
	}
	return out
}

func (s *Session) resetStatus() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	snap := snapshotStatus(s.agentStatus)
	s.agentStatus = core.AgentStatus{State: "idle"}
	restoreStatus(&s.agentStatus, snap)
	s.turnStart = time.Time{}
	s.toolCount = 0
	s.currentTool = time.Time{}
	s.agentStatus.CurrentToolCommand = ""
}

func (s *Session) startTurnStatus() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	snap := snapshotStatus(s.agentStatus)
	s.agentStatus = core.AgentStatus{State: "thinking"}
	restoreStatus(&s.agentStatus, snap)
	s.turnStart = time.Now()
	s.toolCount = 0
	s.currentTool = time.Time{}
	s.agentStatus.CurrentToolCommand = ""
}

// statusSnapshot holds session-level fields that survive turn resets.
type statusSnapshot struct {
	model           string
	reasoningEffort string
	contextUsed     int
	contextSize     int
}

func snapshotStatus(st core.AgentStatus) statusSnapshot {
	return statusSnapshot{
		model:           st.Model,
		reasoningEffort: st.ReasoningEffort,
		contextUsed:     st.ContextUsed,
		contextSize:     st.ContextSize,
	}
}

func restoreStatus(st *core.AgentStatus, snap statusSnapshot) {
	st.Model = snap.model
	st.ReasoningEffort = snap.reasoningEffort
	st.ContextUsed = snap.contextUsed
	st.ContextSize = snap.contextSize
}

func (s *Session) setToolStatus(active bool, command string) {
	s.statusMu.Lock()
	old := s.agentStatus.State
	if active {
		s.agentStatus.State = "using_tools"
		s.toolCount++
		s.currentTool = time.Now()
		s.agentStatus.CurrentToolCommand = command
	} else {
		s.agentStatus.State = "thinking"
		s.currentTool = time.Time{}
		s.agentStatus.CurrentToolCommand = ""
	}
	s.statusMu.Unlock()
	state := s.agentStatus.State
	if old != state {
		slog.Info("acp: turn status changed", "session", s.sessionKey, "from", old, "to", state)
	}
}

func (s *Session) setUsage(pr promptResult) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.agentStatus.InputTokens = pr.Usage.InputTokens
	s.agentStatus.OutputTokens = pr.Usage.OutputTokens
}

func (s *Session) Close() error {
	if s.tr == nil {
		return nil
	}
	err := s.tr.Close()
	if s.OnClose != nil {
		s.OnClose()
	}
	return err
}

// buildPromptWithFiles appends file descriptions or text-file contents to the prompt.
// Binary files are described by name and MIME type; text-ish files include a short
// content preview (capped so the prompt does not explode).
func buildPromptWithFiles(prompt string, files []core.FileAttachment) string {
	if len(files) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	for _, f := range files {
		if f.FileName == "" && len(f.Data) == 0 {
			continue
		}
		b.WriteString("\n\n[attached file: ")
		if f.FileName != "" {
			b.WriteString(f.FileName)
			if f.MimeType != "" {
				b.WriteString(" (")
				b.WriteString(f.MimeType)
				b.WriteString(")")
			}
		} else {
			b.WriteString("unnamed")
		}
		b.WriteString("]")
		if isTextFile(f.MimeType, f.FileName) && utf8.Valid(f.Data) {
			const maxFileBytes = 50000
			content := string(f.Data)
			if len(f.Data) > maxFileBytes {
				content = string(f.Data[:maxFileBytes]) + "\n... (truncated)"
			}
			b.WriteString("\n")
			b.WriteString(content)
		}
	}
	return b.String()
}

// isTextFile returns true if the file is likely text based on its MIME type or extension.
func isTextFile(mt, filename string) bool {
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch mt {
	case "application/json", "application/javascript", "application/xml", "application/x-yaml", "application/x-shellscript", "application/x-httpd-php":
		return true
	}
	for _, ext := range []string{".txt", ".md", ".markdown", ".json", ".js", ".ts", ".jsx", ".tsx", ".yaml", ".yml", ".xml", ".html", ".htm", ".css", ".scss", ".sass", ".go", ".py", ".pyw", ".sh", ".bash", ".zsh", ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp", ".java", ".rs", ".rb", ".php", ".sql", ".csv", ".tsv", ".log", ".ini", ".conf", ".toml", ".cfg"} {
		if strings.HasSuffix(strings.ToLower(filename), ext) {
			return true
		}
	}
	return false
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

// extractAgentThought pulls assistant reasoning/thought text out of a session/update notification.
func extractAgentThought(params json.RawMessage) string {
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
	if head.SessionUpdate == "agent_thought_chunk" && head.Content.Type == "text" {
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

func hasToolCall(params json.RawMessage) bool {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return false
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return false
	}
	return head.SessionUpdate == "tool_call" && head.ToolCallID != ""
}

func toolCallKind(params json.RawMessage) string {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(wrap.Update, &head)
	return head.Kind
}

func toolCallCommand(params json.RawMessage) string {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		RawInput json.RawMessage `json:"rawInput"`
	}
	_ = json.Unmarshal(wrap.Update, &head)
	return extractCommandFromRawInput(head.RawInput)
}

func extractCommandFromRawInput(raw json.RawMessage) string {
	var input struct {
		Command   string `json:"command"`
		Cmd       string `json:"cmd"`
		Name      string `json:"name"`
		Function  string `json:"function"`
		Operation string `json:"operation"`
		Tool      string `json:"tool"`
		Action    string `json:"action"`
		Method    string `json:"method"`
		Type      string `json:"type"`
	}
	_ = json.Unmarshal(raw, &input)
	if input.Command != "" {
		return input.Command
	}
	if input.Cmd != "" {
		return input.Cmd
	}
	if input.Name != "" {
		return input.Name
	}
	if input.Function != "" {
		return input.Function
	}
	if input.Operation != "" {
		return input.Operation
	}
	if input.Tool != "" {
		return input.Tool
	}
	if input.Action != "" {
		return input.Action
	}
	if input.Method != "" {
		return input.Method
	}
	return input.Type
}

func toolCallPath(params json.RawMessage) string {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		RawInput  json.RawMessage `json:"rawInput"`
		Locations []struct {
			Path string `json:"path"`
		} `json:"locations"`
	}
	_ = json.Unmarshal(wrap.Update, &head)
	path := extractPathFromRawInput(head.RawInput)
	if path == "" && len(head.Locations) > 0 {
		path = head.Locations[0].Path
	}
	return path
}

func isToolCallCompleted(params json.RawMessage) bool {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return false
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return false
	}
	return head.SessionUpdate == "tool_call_update" && head.ToolCallID != "" && head.Status == "completed"
}

// extractToolRawInput returns the raw input of a tool_call as an indented JSON string.
// It falls back to arguments/params if rawInput is absent.
func extractToolRawInput(params json.RawMessage) string {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		RawInput  json.RawMessage `json:"rawInput"`
		Arguments json.RawMessage `json:"arguments"`
		Params    json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return ""
	}
	if raw := bytes.TrimSpace(head.RawInput); len(raw) > 0 {
		return prettyJSON(raw)
	}
	if raw := bytes.TrimSpace(head.Arguments); len(raw) > 0 {
		return prettyJSON(raw)
	}
	return prettyJSON(head.Params)
}

// extractToolRawOutput returns the raw output of a tool_call_update as an indented JSON string.
func extractToolRawOutput(params json.RawMessage) string {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return ""
	}
	var head struct {
		RawOutput json.RawMessage `json:"rawOutput"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		return ""
	}
	return prettyJSON(head.RawOutput)
}

func prettyJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
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

func truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
