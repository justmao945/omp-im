package agent

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/justmao945/omp-im/internal/core"
)

func writeLine(t *testing.T, path, line string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListOMPSessionsFiltersByCwd(t *testing.T) {
	root := t.TempDir()
	workDir := "/Users/x/Code/omp-im"

	// Session 1: matches cwd, has a title.
	s1 := filepath.Join(root, "-Users-x-Code-omp-im", "2026-07-13T03-44-29Z_abc12345.jsonl")
	writeLine(t, s1, `{"type":"title","title":"Fix startup exit"}`)
	appendLine(t, s1, `{"type":"session","version":3,"id":"abc12345-aaaa-bbbb-cccc-dddddddddddd","timestamp":"2026-07-13T03:44:29.813Z","cwd":"/Users/x/Code/omp-im"}`)

	// Session 2: different cwd, must be filtered out.
	s2 := filepath.Join(root, "-Users-x-Code-other", "2026-07-13T04-00-00Z_fff.jsonl")
	writeLine(t, s2, `{"type":"title","title":"Other"}`)
	appendLine(t, s2, `{"type":"session","version":3,"id":"ffffffff-0000","timestamp":"2026-07-13T04:00:00Z","cwd":"/Users/x/Code/other"}`)

	// Session 3: matches cwd, no title (should fall back to empty).
	s3 := filepath.Join(root, "-Users-x-Code-omp-im", "2026-07-13T05-00-00Z_def67890.jsonl")
	writeLine(t, s3, `{"type":"session","version":3,"id":"def67890-aaaa","timestamp":"2026-07-13T05:00:00Z","cwd":"/Users/x/Code/omp-im"}`)

	got, err := listOMPSessions(root, workDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(got), got)
	}
	// collectJSOQNL sorts by mtime desc; both s1 and s3 share the same forced
	// mtime pattern only if timestamps differ. Sort by ID for determinism.
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	if got[0].ID != "abc12345-aaaa-bbbb-cccc-dddddddddddd" {
		t.Fatalf("got[0].ID = %q", got[0].ID)
	}
	if got[0].Title != "Fix startup exit" {
		t.Fatalf("got[0].Title = %q", got[0].Title)
	}
}

func TestListClaudeSessionsEncodesCwd(t *testing.T) {
	root := t.TempDir()
	workDir := "/Users/x/Code/omp-im"
	encoded := "-Users-x-Code-omp-im"

	// Session with a real user message.
	s1 := filepath.Join(root, encoded, "11111111-2222-3333-4444-555555555555.jsonl")
	writeLine(t, s1, `{"type":"mode","mode":"normal","sessionId":"11111111-2222-3333-4444-555555555555"}`)
	appendLine(t, s1, `{"type":"user","message":{"role":"user","content":"Refactor the footer builder"}}`)

	// Session whose first user message is a local-command wrapper (should be skipped).
	s2 := filepath.Join(root, encoded, "66666666-2222-3333-4444-555555555555.jsonl")
	writeLine(t, s2, `{"type":"mode","mode":"normal","sessionId":"66666666-2222-3333-4444-555555555555"}`)
	appendLine(t, s2, `{"type":"user","message":{"role":"user","content":"<local-command-caveat>caveat</local-command-caveat>"}}`)
	appendLine(t, s2, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Real prompt here"}]}}`)

	got, err := listClaudeSessions(root, workDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	if got[0].Title != "Refactor the footer builder" {
		t.Fatalf("got[0].Title = %q", got[0].Title)
	}
	if got[1].Title != "Real prompt here" {
		t.Fatalf("got[1].Title = %q (should skip caveat wrapper)", got[1].Title)
	}

	// Different cwd -> no directory -> empty.
	got2, err := listClaudeSessions(root, "/nonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatalf("got %d sessions for missing cwd, want 0", len(got2))
	}
}

func TestListCodexSessionsFiltersByCwdAndUsesIndex(t *testing.T) {
	root := t.TempDir()
	idxPath := filepath.Join(t.TempDir(), "session_index.jsonl")
	workDir := "/Users/x/Code/agent-projects"

	// session_index entry for the first session.
	writeLine(t, idxPath, `{"id":"aaa00000-1111-2222-3333-444444444444","thread_name":"调研开源多模型路由","updated_at":"2026-07-10T13:10:16.292Z"}`)

	dayDir := filepath.Join(root, "2026", "07", "10")
	// Session 1: matches cwd.
	s1 := filepath.Join(dayDir, "rollout-2026-07-10T21-09-36-aaa00000-1111-2222-3333-444444444444.jsonl")
	writeLine(t, s1, `{"timestamp":"2026-07-10T13:10:11.745Z","type":"session_meta","payload":{"session_id":"aaa00000-1111-2222-3333-444444444444","cwd":"/Users/x/Code/agent-projects"}}`)

	// Session 2: different cwd, filtered out.
	s2 := filepath.Join(dayDir, "rollout-2026-07-10T22-00-00-bbb11111-2222.jsonl")
	writeLine(t, s2, `{"timestamp":"2026-07-10T22:00:00.000Z","type":"session_meta","payload":{"session_id":"bbb11111-2222","cwd":"/Users/x/elsewhere"}}`)

	// Session 3: matches cwd, no index title.
	s3 := filepath.Join(dayDir, "rollout-2026-07-10T23-00-00-ccc22222-3333.jsonl")
	writeLine(t, s3, `{"timestamp":"2026-07-10T23:00:00.000Z","type":"session_meta","payload":{"session_id":"ccc22222-3333","cwd":"/Users/x/Code/agent-projects"}}`)

	got, err := listCodexSessions(root, idxPath, workDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(got), got)
	}
	byID := map[string]core.SessionInfo{}
	for _, s := range got {
		byID[s.ID] = s
	}
	if byID["aaa00000-1111-2222-3333-444444444444"].Title != "调研开源多模型路由" {
		t.Fatalf("title = %q", byID["aaa00000-1111-2222-3333-444444444444"].Title)
	}
	if byID["ccc22222-3333"].Title != "" {
		t.Fatalf("expected empty title for unindexed session, got %q", byID["ccc22222-3333"].Title)
	}
}

func TestCleanPreviewTruncates(t *testing.T) {
	long := make([]rune, 200)
	for i := range long {
		long[i] = 'x'
	}
	got := cleanPreview(string(long))
	if len([]rune(got)) > 60 {
		t.Fatalf("cleanPreview returned %d runes, want <=60", len([]rune(got)))
	}
	if got := cleanPreview("  hello\n world  "); got != "hello world" {
		t.Fatalf("cleanPreview whitespace = %q", got)
	}
	if got := cleanPreview(""); got != "" {
		t.Fatalf("cleanPreview empty = %q", got)
	}
}

// appendLine appends a single JSON line to an existing file.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}
