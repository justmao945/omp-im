package wecom

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/justmao945/omp-im/internal/core"
)

// maxStreamContentBytes is the maximum content length for a single WeCom stream message.
const maxStreamContentBytes = 20480

// sendTextReply sends a passive text reply to the chat that triggered the inbound message.
// Long text is split into multiple stream chunks with the same stream id.
func (p *Platform) sendTextReply(ctx context.Context, rc *replyContext, text string) error {
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
		if err := p.respond(ctx, rc.reqID, body); err != nil {
			return err
		}
	}
	return nil
}

// StreamReply implements core.StreamReplyer. It sends a single stream chunk for
// the current turn. The first call initializes the shared stream id, and
// the finished chunk signals the end of the turn. WeCom stream messages expect
// the content field to be the cumulative text so far, not a delta, so we
// accumulate incoming deltas in the reply context.
func (p *Platform) StreamReply(ctx context.Context, replyCtx any, delta string, finished bool) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.streamID == "" {
		rc.streamID = generateReqID()
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
		p.startStreamTicker(ctx, rc)
	}
	if finished {
		rc.finished = true
	}

	// Initial empty-delta frame: send content="" with finish=false to create
	// the message bubble and trigger the WeCom client's typing animation.
	// Subsequent renders use the status line / body text.
	if delta == "" && !finished && !rc.sentInitialEmpty && rc.streamText == "" && rc.thinkingText == "" && rc.toolName == "" {
		rc.sentInitialEmpty = true
		return p.renderStreamEmpty(ctx, rc)
	}

	if rc.thinkingText != "" && rc.thinkingEnd.IsZero() {
		rc.thinkingEnd = time.Now()
	}

	// ACP chunks are deltas, but the WeCom stream protocol expects each frame
	// to contain the full message content so far (refresh mode). Detect if the
	// agent already sent cumulative text and fall back to appending raw deltas.
	if delta != "" {
		if rc.streamText == "" {
			rc.streamText = delta
		} else if strings.HasPrefix(delta, rc.streamText) {
			rc.streamText = delta
		} else {
			rc.streamText += delta
		}

		// Keep the detailed-mode stream body in the same order as events arrive.
		if idx := streamSectionIndex(rc.streamBody, "text"); idx == -1 {
			rc.streamBody = append(rc.streamBody, streamSection{kind: "text", text: delta})
		} else {
			existing := rc.streamBody[idx].text
			if strings.HasPrefix(delta, existing) {
				rc.streamBody[idx].text = delta
			} else {
				rc.streamBody[idx].text += delta
			}
		}
	}

	return p.renderStream(ctx, rc, finished)
}

// StreamEvent implements core.StreamReplyer. It handles non-text events such as
// thinking and tool status updates and refreshes the stream message.
func (p *Platform) StreamEvent(ctx context.Context, replyCtx any, ev core.StreamEvent) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wecom: invalid reply context")
	}
	if rc.chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.streamID == "" {
		rc.streamID = generateReqID()
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
		p.startStreamTicker(ctx, rc)
	}

	switch ev.Type {
	case "processing":
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
		if ev.Status.ContextSize > 0 {
			rc.contextUsed = ev.Status.ContextUsed
			rc.contextSize = ev.Status.ContextSize
		}
	case "usage":
		if ev.Status.ContextSize > 0 {
			rc.contextUsed = ev.Status.ContextUsed
			rc.contextSize = ev.Status.ContextSize
		}
	case "thinking":
		rc.thinkingText += ev.Text
		if rc.thinkingEnd.IsZero() {
			// Current thinking section is still open; append to it.
			if idx := lastStreamSectionIndex(rc.streamBody, "thinking"); idx != -1 {
				rc.streamBody[idx].text += ev.Text
			} else {
				rc.streamBody = append(rc.streamBody, streamSection{kind: "thinking", text: ev.Text})
			}
		} else {
			// Thinking was closed by a tool or body text; start a new section.
			rc.streamBody = append(rc.streamBody, streamSection{kind: "thinking", text: ev.Text})
			rc.thinkingEnd = time.Time{}
		}
		if ev.Status.ContextSize > 0 {
			rc.contextUsed = ev.Status.ContextUsed
			rc.contextSize = ev.Status.ContextSize
		}
	case "tool_start":
		if rc.thinkingText != "" && rc.thinkingEnd.IsZero() {
			rc.thinkingEnd = time.Now()
		}
		if ev.Status.ContextSize > 0 {
			rc.contextUsed = ev.Status.ContextUsed
			rc.contextSize = ev.Status.ContextSize
		}
		rc.toolName = ev.Tool
		rc.toolStart = time.Now()
		rc.toolHistory = append(rc.toolHistory, toolRecord{
			name:  ev.Tool,
			input: ev.ToolInput,
			start: time.Now(),
		})
		toolText := fmt.Sprintf("🔧 %s", ev.Tool)
		if preview := formatToolInputOneLineFiltered(ev.ToolInput, ev.Tool); preview != "" {
			toolText += "\n" + preview
		}
		rc.streamBody = append(rc.streamBody, streamSection{kind: "tool", text: toolText})
	case "tool_end":
		if !rc.toolStart.IsZero() {
			rc.toolTotalDuration += time.Since(rc.toolStart)
		}
		if ev.Status.ContextSize > 0 {
			rc.contextUsed = ev.Status.ContextUsed
			rc.contextSize = ev.Status.ContextSize
		}
		rc.toolCount++
		rc.toolName = ""
		rc.toolStart = time.Time{}
		if n := len(rc.toolHistory); n > 0 {
			rc.toolHistory[n-1].output = ev.ToolOutput
			rc.toolHistory[n-1].end = time.Now()
		}
		// Detailed mode shows only the call (name + input), not the result.
	}

	return p.renderStream(ctx, rc, false)
}

// startStreamTicker starts a goroutine that re-renders the status line every
// second so the elapsed time ticks smoothly even when no new events arrive.
func (p *Platform) startStreamTicker(ctx context.Context, rc *replyContext) {
	if rc.stopTicker != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	done := make(chan struct{})
	rc.stopTicker = func() {
		ticker.Stop()
		close(done)
	}
	go func() {
		for {
			select {
			case <-ticker.C:
				rc.mu.Lock()
				if rc.streamText == "" && !rc.finished {
					_ = p.renderStream(ctx, rc, false)
				}
				rc.mu.Unlock()
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()
}

// renderStreamEmpty sends a stream frame with empty content and finish=false.
// This creates the message bubble in the WeCom client and triggers the
// native "typing" animation (three dots) before any text arrives.
func (p *Platform) renderStreamEmpty(ctx context.Context, rc *replyContext) error {
	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      rc.streamID,
			"finish":  false,
			"content": "",
		},
	}
	if rc.reqID != "" {
		if err := p.respond(ctx, rc.reqID, body); err != nil {
			return err
		}
	} else {
		if err := p.sendActiveMessage(ctx, rc.chatid, rc.chattype, body); err != nil {
			return err
		}
	}
	rc.lastRender = time.Now()
	return nil
}

// renderStream builds the current stream content and sends it to WeCom.
// Non-final updates are throttled to at most one render per second.
func (p *Platform) renderStream(ctx context.Context, rc *replyContext, finished bool) error {
	if rc.streamID == "" {
		rc.streamID = generateReqID()
	}
	if finished {
		rc.finished = true
		rc.turnEnd = time.Now()
	} else if !rc.lastRender.IsZero() && time.Since(rc.lastRender) < time.Second {
		return nil
	}

	// Stop the ticker once the body has arrived or the turn has finished.
	if (rc.streamText != "" || finished) && rc.stopTicker != nil {
		rc.stopTicker()
		rc.stopTicker = nil
	}

	content := buildStreamContent(rc, p.currentDisplay(), finished, p.currentFooter())

	// Skip non-final frames whose content hasn't changed since the last render.
	// This prevents the per-second status-line ticker from spamming identical
	// frames during long thinking or tool-call phases where no body text has
	// arrived yet and no time-based status line is present.
	if !finished && content == rc.lastContent {
		return nil
	}
	rc.lastContent = content

	// If content fits in one frame, send it directly.
	if len(content) <= maxStreamContentBytes {
		return p.sendStreamFrame(ctx, rc, rc.streamID, content, finished)
	}

	// Content exceeds 20KB: finalize the current stream and start new ones.
	// Each chunk becomes its own complete message bubble (finish=true).
	// If this is the final render, the last chunk carries finish=finished.
	chunks := splitTextChunks(content, maxStreamContentBytes)
	for i, chunk := range chunks {
		sid := rc.streamID
		if i > 0 {
			sid = generateReqID()
		}
		chunkFinish := true
		if i == len(chunks)-1 {
			chunkFinish = finished
		}
		if err := p.sendStreamFrame(ctx, rc, sid, chunk, chunkFinish); err != nil {
			return err
		}
	}
	// Point rc.streamID at a new stream so the next render continues fresh.
	if !finished {
		rc.streamID = generateReqID()
	}
	rc.lastRender = time.Now()
	slog.Debug("wecom: stream split", "chunks", len(chunks), "finished", finished)
	return nil
}

// sendStreamFrame sends a single stream frame and updates lastRender.
func (p *Platform) sendStreamFrame(ctx context.Context, rc *replyContext, streamID, content string, finish bool) error {
	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      streamID,
			"finish":  finish,
			"content": content,
		},
	}
	if rc.reqID != "" {
		if err := p.respond(ctx, rc.reqID, body); err != nil {
			return err
		}
	} else {
		if err := p.sendActiveMessage(ctx, rc.chatid, rc.chattype, body); err != nil {
			return err
		}
	}
	rc.lastRender = time.Now()
	return nil
}

// lastStreamSectionIndex returns the last index of a section with the given kind,
// or -1 if none exists.
func lastStreamSectionIndex(sections []streamSection, kind string) int {
	for i := len(sections) - 1; i >= 0; i-- {
		if sections[i].kind == kind {
			return i
		}
	}
	return -1
}

// streamSectionIndex returns the first index of a section with the given kind,
// or -1 if none exists.
func streamSectionIndex(sections []streamSection, kind string) int {
	for i, s := range sections {
		if s.kind == kind {
			return i
		}
	}
	return -1
}

// quoteText prefixes every line of text with a markdown blockquote marker.
func quoteText(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// buildStreamContent assembles the visible text with optional status line and footer.
// When display is empty, only body text is shown (the "..." animation fills the gap).
// When display is "full", thinking, tool calls, and text are shown in arrival order.
func buildStreamContent(rc *replyContext, display string, finished, footerEnabled bool) string {
	var parts []string

	if display == "full" {
		for _, section := range rc.streamBody {
			switch section.kind {
			case "text":
				parts = append(parts, section.text)
			case "thinking":
				parts = append(parts, quoteText(section.text))
			case "tool":
				parts = append(parts, quoteText(section.text))
			}
		}
	} else {
		// Default mode: show body text only; empty means "..." animation.
		if rc.streamText != "" {
			parts = append(parts, rc.streamText)
		}
	}

	if finished && footerEnabled {
		if f := buildStreamFooter(rc, display); f != "" {
			parts = append(parts, f)
		}
	}

	return strings.Join(parts, "\n\n")
}

// buildStreamFooter builds the summary footer shown at the end of a turn.
// It shows total elapsed time and context usage: ⏱️ Xs · 🧠 X%.
func buildStreamFooter(rc *replyContext, display string) string {
	var d time.Duration
	if !rc.turnEnd.IsZero() && rc.turnEnd.After(rc.turnStart) {
		d = rc.turnEnd.Sub(rc.turnStart)
	} else if !rc.turnStart.IsZero() {
		d = time.Since(rc.turnStart)
	} else {
		return ""
	}
	return core.BuildFooter(core.FooterInfo{
		Duration:    d,
		ContextUsed: rc.contextUsed,
		ContextSize: rc.contextSize,
		ToolCount:   rc.toolCount,
		ShowTools:   display == "full",
	})
}

// buildToolDetails renders a detailed log of every tool call for the footer.
// Currently unused: footer only shows the summary line regardless of display mode.
func buildToolDetails(rc *replyContext) string {
	if len(rc.toolHistory) == 0 {
		return ""
	}
	var parts []string
	for _, rec := range rc.toolHistory {
		elapsed := "0s"
		if !rec.start.IsZero() {
			if rec.end.IsZero() {
				elapsed = formatDuration(time.Since(rec.start))
			} else {
				elapsed = formatDuration(rec.end.Sub(rec.start))
			}
		}
		line := fmt.Sprintf("🔧 %s (%s)", rec.name, elapsed)
		if rec.input != "" {
			line += "\n```\n" + rec.input + "\n```"
		}
		if rec.output != "" {
			line += "\n→\n```\n" + rec.output + "\n```"
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n\n")
}

// formatToolInputOneLine parses a tool-call input JSON and returns a compact,
// single-line representation like "key=value key2=value2" or "cmd arg1 arg2".
// It handles nested argument objects (arguments/args/params/input/parameters)
// and falls back to the first line for non-JSON values.
func formatToolInputOneLine(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	var raw any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		if i := strings.IndexByte(input, '\n'); i != -1 {
			return input[:i]
		}
		return input
	}

	switch v := raw.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		flat := flattenToolInput(v)
		// Special-case command-line tools: show "cmd arg1 arg2".
		if cmd, ok := flat["command"].(string); ok && cmd != "" {
			if args, ok := toolArgsToStrings(flat["args"]); ok && len(args) > 0 {
				return cmd + " " + strings.Join(args, " ")
			}
			return cmd
		}
		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if k == "command" || k == "args" {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%v", k, flat[k]))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatToolInputOneLineFiltered is like formatToolInputOneLine but skips
// key=value pairs whose value already appears in the tool display name,
// avoiding redundant information in the stream output.
func formatToolInputOneLineFiltered(input string, toolDisplay string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	raw, ok := parseToolInputJSON(input)
	if !ok {
		// Non-JSON input: return first line only if not in toolDisplay.
		line := strings.SplitN(input, "\n", 2)[0]
		if strings.Contains(toolDisplay, line) {
			return ""
		}
		return line
	}
	flat := flattenToolInput(raw)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "command" || k == "args" {
			// These are already represented via toolCallCommand in the display.
			if strings.Contains(toolDisplay, fmt.Sprintf("%v", flat[k])) {
				continue
			}
		}
		val := fmt.Sprintf("%v", flat[k])
		// Skip if the value is already part of the tool display name.
		if val != "" && strings.Contains(toolDisplay, val) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, val))
	}
	return strings.Join(parts, " ")
}

func flattenToolInput(obj map[string]any) map[string]any {
	flat := make(map[string]any, len(obj))
	for k, v := range obj {
		nested, ok := v.(map[string]any)
		if ok && isToolInputNestedKey(k) {
			for nk, nv := range nested {
				flat[nk] = nv
			}
			continue
		}
		flat[k] = v
	}
	return flat
}

func isToolInputNestedKey(k string) bool {
	switch k {
	case "arguments", "args", "params", "input", "parameters":
		return true
	}
	return false
}

func toolArgsToStrings(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	args := make([]string, 0, len(arr))
	for _, a := range arr {
		if s, ok := a.(string); ok {
			args = append(args, s)
		}
	}
	return args, true
}

func parseToolInputJSON(input string) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(input), &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// formatDuration returns a human-readable duration rounded to seconds.
// Values under a minute are shown as seconds; otherwise minutes and seconds.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs = secs % 60
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncateUTF8 truncates s to at most maxBytes bytes, never splitting a multi-byte rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backwards from the limit to find a valid rune boundary.
	end := maxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return s[:end]
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

// respond sends an aibot_respond_msg frame using the original inbound req_id
// and waits for the server ack before returning.
func (p *Platform) respond(ctx context.Context, reqID string, body map[string]interface{}) error {
	payload := map[string]interface{}{
		"cmd": "aibot_respond_msg",
		"headers": map[string]string{
			"req_id": reqID,
		},
		"body": body,
	}

	if err := p.wsClient.writeAndWaitAck(ctx, payload, reqID); err != nil {
		return err
	}
	if stream, ok := body["stream"].(map[string]interface{}); ok {
		content, _ := stream["content"].(string)
		finish, _ := stream["finish"].(bool)
		slog.Info("wecom: sent stream frame", "req_id", reqID, "finish", finish, "content_len", len(content), "content_preview", truncateStr(content, 50))
	} else {
		slog.Debug("wecom: sent reply", "req_id", reqID, "msgtype", body["msgtype"])
	}
	return nil
}

// sendActiveMessage is a fallback for active push (aibot_send_msg) when passive reply is not possible.
func (p *Platform) sendActiveMessage(ctx context.Context, chatid, chattype string, body map[string]interface{}) error {
	if chatid == "" {
		return fmt.Errorf("wecom: chatid is empty")
	}

	chatTypeInt := 1
	if chattype == "group" {
		chatTypeInt = 2
	}

	reqID := generateReqID()
	payload := map[string]interface{}{
		"cmd": "aibot_send_msg",
		"headers": map[string]string{
			"req_id": reqID,
		},
		"body": map[string]interface{}{
			"chatid":    chatid,
			"chat_type": chatTypeInt,
		},
	}
	for k, v := range body {
		payload["body"].(map[string]interface{})[k] = v
	}
	return p.wsClient.writeAndWaitAck(ctx, payload, reqID)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
