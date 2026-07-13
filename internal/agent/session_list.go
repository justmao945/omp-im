package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

// ListSessions enumerates the agent CLI's own historical sessions whose
// working directory matches workDir. It reads each agent's on-disk session
// store (the same store the native CLI uses): omp (~/.omp/agent/sessions),
// Claude Code (~/.claude/projects), and Codex (~/.codex/sessions).
func (a *localACPAgent) ListSessions(ctx context.Context, workDir string, limit int) ([]core.SessionInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	h := homeDir()
	switch a.cfg.name {
	case "omp":
		return listOMPSessions(filepath.Join(h, ".omp", "agent", "sessions"), workDir, limit)
	case "claude":
		return listClaudeSessions(filepath.Join(h, ".claude", "projects"), workDir, limit)
	case "codex":
		return listCodexSessions(filepath.Join(h, ".codex", "sessions"), filepath.Join(h, ".codex", "session_index.jsonl"), workDir, limit)
	default:
		return nil, core.ErrNotSupported
	}
}

// sessionFile holds a session jsonl path and its modification time.
type sessionFile struct {
	path  string
	mtime time.Time
}

// collectJSONL walks root recursively and returns all *.jsonl files sorted by
// mtime descending (most recent first).
func collectJSONL(root string) ([]sessionFile, error) {
	var files []sessionFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, sessionFile{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	return files, nil
}

// readHeadLines returns up to the first n lines of path.
func readHeadLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lines []string
	for sc.Scan() && len(lines) < n {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// listOMPSessions scans ~/.omp/agent/sessions for sessions whose cwd matches
// workDir. Each session file starts with an optional title line followed by a
// session line carrying id, timestamp, and cwd.
func listOMPSessions(root, workDir string, limit int) ([]core.SessionInfo, error) {
	files, err := collectJSONL(root)
	if err != nil {
		return nil, err
	}
	var out []core.SessionInfo
	for _, sf := range files {
		lines, err := readHeadLines(sf.path, 4)
		if err != nil || len(lines) == 0 {
			continue
		}
		var id, title string
		for _, line := range lines {
			var head struct {
				Type string `json:"type"`
				// title line
				Title string `json:"title"`
				// session line
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal([]byte(line), &head) != nil {
				continue
			}
			switch head.Type {
			case "title":
				if title == "" {
					title = head.Title
				}
			case "session":
				id = head.ID
				if head.Cwd != workDir {
					id = "" // mismatched cwd; skip
				}
			}
		}
		if id == "" {
			continue
		}
		out = append(out, core.SessionInfo{ID: id, Title: cleanPreview(title), UpdatedAt: sf.mtime})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// listClaudeSessions scans the Claude Code project directory matching workDir.
// Claude encodes the cwd by replacing "/" with "-", so sessions for workDir
// live under ~/.claude/projects/<encoded-cwd>/.
func listClaudeSessions(projectsRoot, workDir string, limit int) ([]core.SessionInfo, error) {
	encoded := strings.ReplaceAll(workDir, "/", "-")
	dir := filepath.Join(projectsRoot, encoded)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []sessionFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, sessionFile{path: filepath.Join(dir, e.Name()), mtime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })

	var out []core.SessionInfo
	for _, sf := range files {
		lines, err := readHeadLines(sf.path, 40)
		if err != nil || len(lines) == 0 {
			continue
		}
		var id string
		var head struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal([]byte(lines[0]), &head) == nil {
			id = head.SessionID
		}
		if id == "" {
			continue
		}
		out = append(out, core.SessionInfo{ID: id, Title: cleanPreview(claudeFirstUserText(lines)), UpdatedAt: sf.mtime})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// claudeFirstUserText extracts text from the first real user message in a
// Claude session jsonl, skipping local-command/system wrappers.
func claudeFirstUserText(lines []string) string {
	for _, line := range lines {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.Type != "user" || msg.Message.Role != "user" {
			continue
		}
		text := extractContentText(msg.Message.Content)
		if text != "" && !strings.HasPrefix(strings.TrimSpace(text), "<") {
			return text
		}
	}
	return ""
}

// listCodexSessions scans ~/.codex/sessions for sessions whose cwd matches
// workDir. Each rollout starts with a session_meta line carrying session_id
// and cwd. Titles come from ~/.codex/session_index.jsonl (thread_name).
func listCodexSessions(sessionsRoot, indexPath, workDir string, limit int) ([]core.SessionInfo, error) {
	files, err := collectJSONL(sessionsRoot)
	if err != nil {
		return nil, err
	}
	titles := loadCodexSessionIndex(indexPath)

	var out []core.SessionInfo
	for _, sf := range files {
		lines, err := readHeadLines(sf.path, 1)
		if err != nil || len(lines) == 0 {
			continue
		}
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(lines[0]), &meta) != nil {
			continue
		}
		if meta.Payload.SessionID == "" || meta.Payload.Cwd != workDir {
			continue
		}
		title := titles[meta.Payload.SessionID]
		out = append(out, core.SessionInfo{ID: meta.Payload.SessionID, Title: cleanPreview(title), UpdatedAt: sf.mtime})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// loadCodexSessionIndex reads ~/.codex/session_index.jsonl into a session_id
func loadCodexSessionIndex(path string) map[string]string {
	m := map[string]string{}
	lines, err := readHeadLines(path, 4096)
	if err != nil {
		return m
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.ID != "" {
			m[entry.ID] = entry.ThreadName
		}
	}
	return m
}

// extractContentText pulls a text string out of a Claude message content field
// which may be a plain string or an array of content blocks.
func extractContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			blk, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := blk["text"].(string); ok && t != "" {
				return t
			}
		}
	}
	return ""
}

// cleanPreview collapses whitespace and truncates a session title for display.
func cleanPreview(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse whitespace runs (including newlines) into single spaces.
	var b strings.Builder
	prevSpace := false
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
	const max = 60
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return s
}
