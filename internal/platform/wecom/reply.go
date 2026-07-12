package wecom

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/justmao945/omp-im/internal/core"
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
	if rc.streamID == "" {
		rc.streamID = generateReqID()
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
	}

	if rc.thinkingText != "" && rc.thinkingEnd.IsZero() {
		rc.thinkingEnd = time.Now()
	}

	// ACP chunks are deltas, but the WeCom stream protocol expects each frame
	// to contain the full message content so far (refresh mode). Detect if the
	// agent already sent cumulative text and fall back to appending raw deltas.
	if rc.streamText == "" {
		rc.streamText = delta
	} else if strings.HasPrefix(delta, rc.streamText) {
		rc.streamText = delta
	} else {
		rc.streamText += delta
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
	if rc.streamID == "" {
		rc.streamID = generateReqID()
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
	}

	switch ev.Type {
	case "thinking":
		rc.thinkingText += ev.Text
	case "tool_start":
		if rc.thinkingText != "" && rc.thinkingEnd.IsZero() {
			rc.thinkingEnd = time.Now()
		}
		rc.toolName = ev.Tool
		rc.toolStart = time.Now()
		rc.toolHistory = append(rc.toolHistory, toolRecord{
			name:  ev.Tool,
			input: ev.ToolInput,
			start: time.Now(),
		})
	case "tool_end":
		if !rc.toolStart.IsZero() {
			rc.toolTotalDuration += time.Since(rc.toolStart)
		}
		rc.toolCount++
		rc.toolName = ""
		rc.toolStart = time.Time{}
		if n := len(rc.toolHistory); n > 0 {
			rc.toolHistory[n-1].output = ev.ToolOutput
			rc.toolHistory[n-1].end = time.Now()
		}
	}

	return p.renderStream(ctx, rc, false)
}

// renderStream builds the current stream content and sends it to WeCom.
// Non-final updates are throttled to at most one render per second.
func (p *Platform) renderStream(ctx context.Context, rc *replyContext, finished bool) error {
	if rc.streamID == "" {
		rc.streamID = generateReqID()
	}
	if finished {
		rc.turnEnd = time.Now()
	} else if !rc.lastRender.IsZero() && time.Since(rc.lastRender) < time.Second {
		return nil
	}

	content := buildStreamContent(rc, p.cfg.thinkingDisplay, p.cfg.toolDisplay, finished)
	body := map[string]interface{}{
		"msgtype": "stream",
		"stream": map[string]interface{}{
			"id":      rc.streamID,
			"finish":  finished,
			"content": content,
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
	rc.lastRender = time.Now()
	if finished {
		slog.Debug("wecom: finished stream reply")
	}
	return nil
}

// buildStreamContent assembles the visible text with optional status line and footer.
// Once the body text starts, the status line is hidden and replaced by the body.
func buildStreamContent(rc *replyContext, thinkingDisplay, toolDisplay string, finished bool) string {
	var parts []string

	if rc.streamText == "" {
		// Status line: only shown while no body text has arrived yet.
		switch {
		case rc.toolName != "" && toolDisplay != "off":
			elapsed := ""
			if !rc.toolStart.IsZero() {
				elapsed = fmt.Sprintf(" %s", formatDuration(time.Since(rc.toolStart)))
			}
			line := fmt.Sprintf("🔧 %s%s", rc.toolName, elapsed)
			if toolDisplay == "detailed" && rc.toolHistory != nil {
				if n := len(rc.toolHistory); n > 0 {
					if preview := truncateText(rc.toolHistory[n-1].input, 120); preview != "" {
						line += "\n```\n" + preview + "\n```"
					}
				}
			}
			parts = append(parts, line)
		case rc.thinkingText != "" && thinkingDisplay != "off":
			if thinkingDisplay == "detailed" {
				parts = append(parts, rc.thinkingText)
			} else {
				elapsed := ""
				if !rc.turnStart.IsZero() {
					elapsed = fmt.Sprintf(" %s", formatDuration(time.Since(rc.turnStart)))
				}
				parts = append(parts, fmt.Sprintf("🤔 thinking%s", elapsed))
			}
		}
	} else {
		// Body text has arrived; show it instead of the status line.
		parts = append(parts, rc.streamText)
	}

	// Footer at the end of the turn.
	if finished {
		if footer := buildStreamFooter(rc, thinkingDisplay, toolDisplay); footer != "" {
			parts = append(parts, footer)
		}
	}

	return strings.Join(parts, "\n\n")
}

// buildStreamFooter builds the summary footer shown at the end of a turn.
func buildStreamFooter(rc *replyContext, thinkingDisplay, toolDisplay string) string {
	var items []string

	hasThinking := thinkingDisplay != "off" && !rc.thinkingEnd.IsZero() && rc.thinkingEnd.After(rc.turnStart)
	if hasThinking {
		items = append(items, fmt.Sprintf("thinking %s", formatDuration(rc.thinkingEnd.Sub(rc.turnStart))))
	}
	hasTools := toolDisplay != "off" && rc.toolCount > 0
	if hasTools {
		items = append(items, fmt.Sprintf("%d tool%s %s", rc.toolCount, plural(rc.toolCount), formatDuration(rc.toolTotalDuration)))
	}
	if len(items) == 0 {
		return ""
	}
	if !rc.turnEnd.IsZero() && rc.turnEnd.After(rc.turnStart) {
		items = append(items, fmt.Sprintf("total %s", formatDuration(rc.turnEnd.Sub(rc.turnStart))))
	} else if !rc.turnStart.IsZero() {
		items = append(items, fmt.Sprintf("total %s", formatDuration(time.Since(rc.turnStart))))
	}

	footer := "> " + strings.Join(items, " · ")
	if toolDisplay == "detailed" {
		if details := buildToolDetails(rc); details != "" {
			footer += "\n\n" + details
		}
	}
	return footer
}

// buildToolDetails renders a detailed log of every tool call for the footer.
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

// truncateText returns a prefix of text up to max runes, appending "..." if truncated.
func truncateText(text string, maxRunes int) string {
	if len(text) == 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	return string([]rune(text)[:maxRunes]) + "..."
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
