package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
)

func TestCleanPreviewTruncates(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := cleanPreview(long)
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

// fakeLister is a localACPAgent substitute that returns a canned list,
// exercising only the SessionLister contract the engine depends on.
type fakeLister struct {
	name     string
	sessions []core.SessionInfo
	err      error
}

func (f *fakeLister) ListSessions(ctx context.Context, workDir string, limit int) ([]core.SessionInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit <= 0 || limit > len(f.sessions) {
		return f.sessions, nil
	}
	return f.sessions[:limit], nil
}

// Verify the SessionLister contract independently of the real ACP transport.
func TestListSessionsContract(t *testing.T) {
	l := &fakeLister{name: "omp", sessions: []core.SessionInfo{
		{ID: "aaa", Title: "first", UpdatedAt: testTime()},
		{ID: "bbb", Title: "second", UpdatedAt: testTime()},
	}}
	got, err := l.ListSessions(context.Background(), "/tmp", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "aaa" {
		t.Fatalf("got %+v", got)
	}
	// limit truncation
	got2, _ := l.ListSessions(context.Background(), "/tmp", 1)
	if len(got2) != 1 {
		t.Fatalf("limit not applied: %+v", got2)
	}
}

func testTime() time.Time { return time.Time{} }
