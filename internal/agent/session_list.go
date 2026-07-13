package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// claude-agent-acp, codex-acp), initializes it, and calls session/list.
// This is the protocol-native, non-interactive equivalent of the native CLI's
// interactive session picker.
func (a *localACPAgent) ListSessions(ctx context.Context, workDir string, limit int) ([]core.SessionInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	cfg := Config{
		Command:          a.cfg.command,
		Args:             a.cfg.args,
		WorkDir:          workDir,
		AutoApproveTools: a.cfg.autoApproveTools,
		AuthMethod:       a.cfg.authMethod,
		InstallHint:      a.cfg.installHint,
	}
	tr, err := NewTransport(cfg, nil)
	if err != nil {
		return nil, err
	}
	defer tr.Close()

	// initialize — exchange capabilities and detect session/list support.
	initRes, err := tr.call(ctx, "initialize", map[string]any{
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
	})
	if err != nil {
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	var initOut struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				List json.RawMessage `json:"list"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(initRes, &initOut); err != nil {
		return nil, fmt.Errorf("acp parse initialize: %w", err)
	}
	if len(initOut.AgentCapabilities.SessionCapabilities.List) == 0 {
		return nil, fmt.Errorf("agent %q does not support session/list", a.cfg.name)
	}

	// Authenticate when the adapter requires an ACP auth method (omp does;
	// claude/codex rely on their own CLI credentials).
	if cfg.AuthMethod != "" {
		if _, err := tr.call(ctx, "authenticate", map[string]any{"methodId": cfg.AuthMethod}); err != nil {
			slog.Debug("acp list: authenticate skipped", "agent", a.cfg.name, "error", err)
		}
	}

	// session/list — filter by cwd. The adapter returns its own default page;
	// we truncate to limit. Cursor pagination is available but not needed for
	// the typical history browse.
	listRes, err := tr.call(ctx, "session/list", map[string]any{"cwd": workDir})
	if err != nil {
		return nil, fmt.Errorf("acp session/list: %w", err)
	}
	var out struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listRes, &out); err != nil {
		return nil, fmt.Errorf("acp parse session/list: %w", err)
	}

	sessions := make([]core.SessionInfo, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		if s.SessionID == "" {
			continue
		}
		info := core.SessionInfo{ID: s.SessionID, Title: cleanPreview(s.Title)}
		if t, err := time.Parse(time.RFC3339Nano, s.UpdatedAt); err == nil {
			info.UpdatedAt = t
		} else if t, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil {
			info.UpdatedAt = t
		}
		sessions = append(sessions, info)
	}
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

// cleanPreview collapses whitespace and truncates a session title for display.
func cleanPreview(s string) string {
	// Collapse all whitespace runs (including newlines) into single spaces.
	var b strings.Builder
	prevSpace := true
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	s = strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	const max = 60
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return s
}
